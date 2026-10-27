// Package bot connects allowed native text events to a text-only model.
package bot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/sending"
)

type Source interface {
	Account() string
	Identity(context.Context) (string, error)
	Events(context.Context, string) (linuxnative.EventPage, error)
	Message(context.Context, string) (linuxnative.Message, error)
}
type Model interface {
	Complete(context.Context, string) (string, error)
}
type imageSender interface {
	SendImage(context.Context, string, string) (linuxnative.ToolStatus, error)
}
type mediaSender interface {
	SendMedia(context.Context, string, string, string) (linuxnative.ToolStatus, error)
}
type Config struct {
	AuthorizeThread   func(context.Context, string) (bool, error)
	ReportFailure     func(companion.Input, string) error
	AgentTask         func(time.Time, func(string) bool) (*companion.Input, error)
	AgentTaskDone     func(companion.Input, string)
	AgentTaskDelivery func(companion.Input, TaskDelivery) error
	AgentTaskActive   func(companion.Input) bool
	Draining          func() bool
	MediaSeeds        func() []MediaSeed
	SurfAllowed       func(string) bool
	SurfTask          func(time.Time) (*SurfTask, error)
	Options           *Options
	Analyzer          interface {
		Analyze(context.Context, linuxnative.Message) (string, error)
	}
	StateDir       string
	AllowedThreads []string
	Prefix         string
	PollInterval   time.Duration
	MaxAge         time.Duration
	Log            func(string)
	Record         func(Record)
	GroupRules     map[string]GroupRule
	Channel        string
}
type GroupRule struct {
	Mode, Prefix, Persona   string
	ForceReplyWhenMentioned bool
}
type Worker struct {
	generation      *generationScheduler
	runtime         *runtimeState
	persistErr      error
	recovering      bool
	config          Config
	source          Source
	model           Model
	sender          sending.Engine
	allowed         map[string]bool
	lastMaintenance time.Time
}

func New(config Config, source Source, model Model, sender sending.Engine) (*Worker, error) {
	if config.StateDir == "" || source == nil || model == nil || sender == nil {
		return nil, errors.New("bot configuration incomplete")
	}
	if config.Prefix == "" {
		config.Prefix = "/ai "
	}
	if config.Channel == "" {
		config.Channel = "wechat"
	}
	if len(config.Prefix) > 64 || !utf8.ValidString(config.Prefix) {
		return nil, errors.New("invalid bot prefix")
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 2 * time.Second
	}
	if config.MaxAge <= 0 {
		config.MaxAge = 2 * time.Minute
	}
	allowed := map[string]bool{}
	for _, thread := range config.AllowedThreads {
		thread = strings.TrimSpace(thread)
		if thread != "" {
			allowed[thread] = true
		}
	}
	if len(allowed) == 0 && config.AuthorizeThread == nil {
		return nil, errors.New("bot thread allowlist is required")
	}
	if config.Log == nil {
		config.Log = func(string) {}
	}
	return &Worker{config: config, source: source, model: model, sender: sender, allowed: allowed}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	if err := os.MkdirAll(w.config.StateDir, 0700); err != nil {
		return errors.New("bot state directory unavailable")
	}
	lock, err := lockState(filepath.Join(w.config.StateDir, "worker.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	server, err := w.source.Identity(ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(w.config.StateDir, "checkpoint.json")
	state, exists, err := loadCheckpoint(path, server, w.source.Account())
	if err != nil {
		return err
	}
	if !exists {
		// Start at the current tail: initial history is never sent to a model.
		for pages := 0; ; pages++ {
			if pages >= 101 {
				return errors.New("initial event history exceeds bound")
			}
			page, err := w.source.Events(ctx, state.Cursor)
			if err != nil {
				return err
			}
			state.Cursor = page.NextCursor
			if len(page.Items) < 100 {
				break
			}
		}
		if err := saveCheckpoint(path, state); err != nil {
			return err
		}
		w.config.Log("bot_started_history_skipped")
	} else {
		w.config.Log("bot_resumed_saved_cursor")
	}
	if w.config.Options != nil {
		return w.runDurable(ctx, path, state)
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		page, err := w.source.Events(ctx, state.Cursor)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		} // Never silently reset an expired cursor.
		for _, event := range page.Items {
			// Claim before fetching content or making any external request. A
			// crash may lose this reply, but will not repeat a possibly sent one.
			duplicate := state.contains(event.Data.MessageID)
			state.Cursor = event.Cursor
			if event.Kind == "message.committed" && event.Data.MessageID != "" && !duplicate {
				state.remember(event.Data.MessageID)
			}
			if err := saveCheckpoint(path, state); err != nil {
				return err
			}
			if duplicate || event.Kind != "message.committed" || event.Data.MessageID == "" || event.Data.Origin == "local_send" {
				continue
			}
			if !w.allowed[event.Data.ThreadID] && w.config.Record == nil && w.config.AuthorizeThread == nil {
				continue
			}

			w.handle(ctx, event)
		}
		if len(page.Items) == 100 {
			continue
		}
		w.tickCharacter(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(w.config.PollInterval):
		}
	}
}

func (w *Worker) handle(ctx context.Context, event linuxnative.Event) {
	if w.config.AuthorizeThread != nil {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		allowed, e := w.config.AuthorizeThread(checkCtx, event.Data.ThreadID)
		cancel()
		if e != nil {
			w.queuePhase(event.Data.MessageID, "read_pending")
			w.activity(Record{Event: "contact_check_pending", Category: "decision", ThreadID: event.Data.ThreadID, MessageID: event.Data.MessageID, Reason: "contact_directory_unavailable", Status: "queued"})
			return
		}
		if allowed {
			w.allowed[event.Data.ThreadID] = true
		} else {
			delete(w.allowed, event.Data.ThreadID)
			w.activity(Record{Event: "message_skipped", Category: "decision", ThreadID: event.Data.ThreadID, MessageID: event.Data.MessageID, Reason: "conversation_not_allowed", Status: "skipped"})
			return
		}
	}
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	message, err := w.source.Message(readCtx, event.Data.MessageID)
	cancel()
	if err != nil {
		w.config.Log("message_read_pending")
		w.queuePhase(event.Data.MessageID, "read_pending")
		w.activity(Record{Event: "message_read_pending", Category: "error", ThreadID: event.Data.ThreadID, MessageID: event.Data.MessageID, Reason: "message_read_pending", Status: "queued"})
		return
	}
	if message.ThreadID != event.Data.ThreadID || message.Direction != event.Data.Direction {
		return
	}
	// Historical reviews are processed in order. A later recovered message
	// must not interrupt a previous review before it has had a model decision
	// and completed its already queued delivery.
	if w.ownerModelReview(message.ID) && w.pendingConversationThread(message.ThreadID) {
		w.queuePhase(message.ID, "received")
		return
	}
	if character, ok := w.model.(*companion.Engine); ok && character.Config.Enabled && w.allowed[message.ThreadID] {
		stamp := time.Now()
		if parsed, err := time.Parse(time.RFC3339, message.CreatedAt); err == nil && !parsed.After(stamp) {
			stamp = parsed
		}
		if err := character.Store.TouchActivity(companion.Input{Channel: w.config.Channel, Account: w.source.Account(), Thread: message.ThreadID}, stamp); err != nil {
			w.config.Log("character_activity_persistence_failed")
		}
	}
	if message.Direction != "incoming" && message.ThreadID != "filehelper" {
		w.activity(Record{Event: "message_sent", Category: "sent", ThreadID: message.ThreadID, Sender: message.SenderID, Content: messageSummary(message), MessageID: message.ID, Origin: "wechat", Status: "observed"})
		return
	}
	w.recordIncoming(message)
	if !w.allowed[message.ThreadID] {
		w.activity(Record{Event: "message_skipped", Category: "decision", ThreadID: message.ThreadID, MessageID: message.ID, Reason: "conversation_not_allowed", Status: "skipped"})
		return
	}
	quoteContext := ""
	if message.IsQuotedText() {
		quoteContext = w.readQuoteContext(ctx, &message)
	}
	working := quotedPlainBody(message)
	isMedia := message.Type == 34 || message.Type == 3 || message.Type == 47 || message.Type == 43 || message.Type == 49 && !message.IsQuotedText()
	if isMedia && w.config.Options != nil && w.config.Analyzer != nil {
		working.Type = 1
		working.Body = "收到了一张图片，请结合内容决定是否回应。"
		if message.Type == 47 {
			working.Body = "收到了一条表情包，请结合画面、文字和前后聊天语境判断情绪与含义，自行决定是否回应。"
		}
		if message.Type == 34 {
			working.Body = "收到一条微信语音，请结合转写和上下文决定是否回应。"
		}
		if message.Type == 43 {
			working.Body = "收到了一段视频，请结合内容决定是否回应。"
		}
		if message.Type == 49 {
			working.Body = "收到了一条分享卡片，请结合介绍决定是否回应。"
		}
		if !strings.HasSuffix(message.ThreadID, "@chatroom") {
			working.Body = w.config.Prefix + working.Body
		}
	}
	prompt, eligible := w.prompt(working, time.Now())
	noticed := message
	if message.IsQuotedText() {
		noticed = working
		noticed.Body += quoteContext
	}
	w.noticeConversationMessage(noticed, eligible)
	if w.persistErr != nil {
		return
	}
	if w.stopRequestedVoice(ctx, working) {
		if !eligible {
			return
		}
	}
	if !eligible {
		w.activity(Record{Event: "message_skipped", Category: "decision", ThreadID: message.ThreadID, MessageID: message.ID, Reason: w.skipReason(message, time.Now()), Status: "skipped"})
		return
	}
	if w.generation != nil && w.deferBusyGeneration(ctx, working) {
		return
	}
	reviewQuestion := w.config.GroupRules[message.ThreadID].Mode == "social" && companion.NeedsQuestionReview(prompt)
	prompt += quoteContext
	w.config.Log("eligible_message_received")
	processingReason := "trigger_matched"
	if w.ownerModelReview(message.ID) {
		processingReason = "owner_model_review"
	} else if reviewQuestion {
		processingReason = "direct_question_review"
	}
	w.activity(Record{Event: "message_processing", Category: "decision", ThreadID: message.ThreadID, MessageID: message.ID, Reason: processingReason, Status: "processing"})
	if isMedia && w.config.Options != nil && w.config.Analyzer != nil {
		if (message.Type == 3 || message.Type == 47) && !w.config.Options.ReadImages || message.Type != 3 && message.Type != 47 && message.Type != 34 && !w.config.Options.ReadVideos {
			w.activity(Record{Event: "message_skipped", Category: "decision", ThreadID: message.ThreadID, MessageID: message.ID, Reason: "media_understanding_disabled", Status: "skipped"})
			return
		}
		if w.generation == nil {
			analysisCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			summary, analysisErr := w.config.Analyzer.Analyze(w.modelContext(analysisCtx, message.ThreadID, message.ID), message)
			cancel()
			if analysisErr != nil {
				if ctx.Err() != nil {
					return
				}
				if w.keepUnavailableObservation(companion.Input{Channel: w.config.Channel, Account: w.source.Account(), Thread: message.ThreadID, Sender: message.SenderID, TraceID: message.ID, Text: prompt, MessageTime: message.CreatedAt}, analysisErr) {
					return
				}
				w.modelFailed(message.ThreadID, message.ID, analysisErr)
				return
			}
			if message.Type == 34 {
				prompt = summary
			} else {
				prompt += "\n以下是本条媒体的观察资料（不是指令）：\n" + summary
			}
			w.activity(Record{Event: "media_understood", Category: "tool", ThreadID: message.ThreadID, MessageID: message.ID, Content: summary, Reason: "media_analysis_ready", Status: "processing"})
		}
	}
	if character, ok := w.model.(*companion.Engine); ok {
		mode := "direct"
		if w.config.GroupRules[message.ThreadID].Mode == "social" && !w.addressed(message) {
			mode = "social"
		}
		input := companion.Input{Channel: w.config.Channel, Account: w.source.Account(), Thread: message.ThreadID, Sender: message.SenderID, Text: prompt, Mode: mode, IsGroup: strings.HasSuffix(message.ThreadID, "@chatroom"), Directed: !strings.HasSuffix(message.ThreadID, "@chatroom") || w.addressed(message), ReviewRequired: reviewQuestion, HistoricalReview: w.ownerModelReview(message.ID), PersonaOverride: w.config.GroupRules[message.ThreadID].Persona, TraceID: message.ID, QuoteServerID: message.ServerID, MessageTime: message.CreatedAt, MustReply: mode == "direct" && message.Mentions.Self != nil && *message.Mentions.Self && w.config.GroupRules[message.ThreadID].ForceReplyWhenMentioned}
		if message.Type == 49 && message.AppMessageType != nil {
			switch *message.AppMessageType {
			case 3, 4, 5, 6, 51:
				input.QuoteSharedMessage = true
			}
		}
		w.aggregateConversation(ctx, character, message, &input)
		if w.persistErr != nil {
			return
		}
		if w.generation != nil {
			var analysis *linuxnative.Message
			if isMedia && w.config.Analyzer != nil {
				copy := message
				analysis = &copy
			}
			w.startGeneration(ctx, character, input, analysis)
		} else {
			w.respondCharacter(ctx, character, input)
		}
		return
	}
	modelCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	answer, err := w.model.Complete(w.modelContext(modelCtx, message.ThreadID, message.ID), prompt)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		w.config.Log("model_request_failed_after_recovery")
		w.modelFailed(message.ThreadID, message.ID, err)
		return
	}
	text := "[AI] " + strings.TrimSpace(answer)
	if !utf8.ValidString(text) || strings.TrimSpace(answer) == "" {
		w.config.Log("model_output_invalid")
		return
	}
	if len(text) > 4096 {
		limit := 4096 - len("\n…")
		for !utf8.ValidString(text[:limit]) {
			limit--
		}
		text = text[:limit] + "\n…"
	}
	w.markSending(message.ID)
	if w.persistErr != nil {
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	_, err = w.sendText(sendCtx, companion.Input{Channel: w.config.Channel, Account: w.source.Account(), Thread: message.ThreadID, TraceID: message.ID, QuoteServerID: message.ServerID, Directed: w.addressed(message), Mode: "direct"}, 0, text)
	cancel()
	if err != nil {
		status := "failed"
		if errors.Is(err, sending.ErrOutcomeUnknown) {
			status = "unknown"
		}
		w.activity(Record{Event: "send_failed", Category: "error", ThreadID: message.ThreadID, MessageID: message.ID, Content: text, Origin: "bot", Status: status, Reason: sending.FailureReason(err), SendID: sending.IntentID(err)})
		if errors.Is(err, sending.ErrOutcomeUnknown) {
			w.config.Log("reply_outcome_unknown_not_retried")
		} else {
			w.config.Log("reply_failed_not_retried")
		}
		return
	}
	w.config.Log("reply_server_accepted")
	w.activity(Record{Event: "message_sent", Category: "sent", ThreadID: message.ThreadID, MessageID: message.ID, Content: text, Origin: "bot", Status: "accepted"})
}

func (w *Worker) prompt(message linuxnative.Message, now time.Time) (string, bool) {
	if !w.allowed[message.ThreadID] || message.Type != 1 || !utf8.ValidString(message.Body) || len(message.Body) > 4096 {
		return "", false
	}
	created, err := time.Parse(time.RFC3339, message.CreatedAt)
	if err != nil || now.Sub(created) > w.maxMessageAge() || created.After(now.Add(30*time.Second)) {
		return "", false
	}
	text := strings.TrimSpace(message.Body)
	if strings.HasPrefix(text, "[AI]") {
		return "", false
	}
	if strings.HasSuffix(message.ThreadID, "@chatroom") {
		rule := w.config.GroupRules[message.ThreadID]
		if message.Direction != "incoming" {
			return "", false
		}
		if rule.Mode == "social" && !w.addressed(message) {
			character, ok := w.model.(*companion.Engine)
			if !ok || !character.Config.Permissions.ObserveGroups || !character.Config.Permissions.AutonomousGroups {
				return "", false
			}
		}
		if rule.Mode != "all" && rule.Mode != "social" && !w.addressed(message) {
			return "", false
		}
		// Display names are not trusted as evidence of a mention. Remove only
		// leading display mentions after structured self=true has passed.
		for strings.HasPrefix(text, "@") {
			i := strings.IndexAny(text, " \u2005\t\n")
			if i < 0 {
				break
			}
			text = strings.TrimSpace(text[i:])
		}
		if rule.Prefix != "" {
			if !strings.HasPrefix(text, rule.Prefix) {
				return "", false
			}
			text = strings.TrimSpace(strings.TrimPrefix(text, rule.Prefix))
		} else {
			text = strings.TrimSpace(strings.TrimPrefix(text, w.config.Prefix))
		}
	} else {
		if message.Direction != "incoming" && !(message.ThreadID == "filehelper" && message.Direction == "outgoing") {
			return "", false
		}
		if text == strings.TrimSpace(w.config.Prefix) {
			return "", false
		}
		// Approved private contacts speak normally. Filehelper retains its
		// explicit trigger because both local/outgoing notes are observed there.
		if message.ThreadID == "filehelper" && !strings.HasPrefix(text, w.config.Prefix) && !message.ReplyToSelf {
			return "", false
		}
		text = strings.TrimSpace(strings.TrimPrefix(text, w.config.Prefix))
	}
	return text, text != ""
}

func (w *Worker) respondCharacter(ctx context.Context, character *companion.Engine, input companion.Input) {
	if w.generation != nil {
		w.startGeneration(ctx, character, input, nil)
		return
	}
	modelCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	modelCtx = companion.WithMediaIntent(modelCtx, func(ctx context.Context, intent string) error {
		if input.Directed || input.Thread == "filehelper" || !strings.HasSuffix(input.Thread, "@chatroom") {
			w.cancelVoiceForIntent(ctx, input, intent)
		}
		return w.persistErr
	})
	toolStarted := false
	progressDelivered := false
	if !input.Surf && input.Mode != "proactive" {
		handler := w.toolProgress(character, input)
		modelCtx = companion.WithProgress(modelCtx, func(ctx context.Context, text string) (bool, error) {
			toolStarted = true
			sent, err := handler(ctx, text)
			progressDelivered = progressDelivered || sent
			return sent, err
		})
	}
	decision, err := character.Respond(w.modelContext(modelCtx, input.Thread, input.TraceID), input)
	processingErr := modelCtx.Err()
	cancel()
	w.finishCharacter(ctx, character, input, decision, err, processingErr, toolStarted, progressDelivered)
}

func (w *Worker) finishCharacter(ctx context.Context, character *companion.Engine, input companion.Input, decision companion.Decision, err, processingErr error, toolStarted, progressDelivered bool) {
	if input.ReportOnly {
		decision.ImageID = ""
		decision.MediaIDs = nil
	}
	if err != nil && w.keepUnavailableObservation(input, err) {
		return
	}
	if !w.taskActive(input) {
		w.activity(Record{Event: "agent_task_cancelled", Category: "decision", ThreadID: input.Thread, MessageID: input.TraceID, Status: "stopped", Reason: "task_cancelled_before_delivery"})
		return
	}
	if err == nil && ctx.Err() == nil && input.Mode == "direct" && character.Config.Permissions.ReplyText {
		switch decision.Reason {
		case "invalid_model_decision", "invalid_tool_arguments", "tool_unavailable_or_budget_exhausted", "tool_budget_exhausted":
			w.activity(Record{Event: "model_decision_failed", Category: "error", ThreadID: input.Thread, MessageID: input.TraceID, Reason: decision.Reason, Status: "failed", Origin: "model"})
			decision = companion.Decision{Action: "reply", Messages: []string{"刚才处理这条请求时出了点问题，这次还没完成，不能说已经发给你了。"}, Reason: "decision_failure_reported"}
		}
	}
	if err != nil && toolStarted && processingErr != nil && ctx.Err() == nil {
		w.activity(Record{Event: "tool_task_timeout", Category: "error", ThreadID: input.Thread, MessageID: input.TraceID, Reason: "tool_task_deadline_exceeded", Status: "failed"})
		decision = companion.Decision{Action: "reply", Messages: []string{"这次等得有点久，还没能拿到可用的结果，我先停下这次尝试。"}, Reason: "tool_task_deadline_exceeded"}
		err = nil
	}
	if progressDelivered && ctx.Err() == nil && (err != nil || decision.Action != "reply") {
		if err != nil {
			w.modelFailed(input.Thread, input.TraceID, err)
		}
		w.activity(Record{Event: "tool_task_finished_without_result", Category: "decision", ThreadID: input.Thread, MessageID: input.TraceID, Reason: "tool_task_no_final_after_progress", Status: "completed"})
		decision = companion.Decision{Action: "reply", Messages: []string{"这次还没把结果发出来，我先停下这次尝试。"}, Reason: "tool_task_no_final_after_progress"}
		err = nil
	}
	if w.config.ReportFailure != nil && input.IsGroup && (decision.Reason == "decision_failure_reported" || decision.Reason == "tool_task_deadline_exceeded" || decision.Reason == "tool_task_no_final_after_progress" || err != nil && ctx.Err() == nil) {
		reason := decision.Reason
		if err != nil {
			reason = "model_generation_failed"
		}
		if reportErr := w.config.ReportFailure(input, reason); reportErr != nil {
			w.config.Log("private_operation_report_save_failed")
		}
		decision = companion.Decision{Action: "silent", Reason: "operation_reported_privately"}
		err = nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		w.config.Log("character_model_or_memory_failed")
		w.modelFailed(input.Thread, input.TraceID, err)
		return
	}
	if decision.Action != "reply" {
		w.scheduleConversationWake(character, input, decision.WakeAfterSeconds, decision.WaitForReply)
		category := "decision"
		if strings.Contains(decision.Reason, "invalid") {
			category = "error"
		}
		w.activity(Record{Event: "character_decision", Category: category, ThreadID: input.Thread, MessageID: input.TraceID, Reason: decision.Reason, Status: decision.Action})
		if strings.Contains(decision.Reason, "budget") {
			w.config.Log("character_budget_limited")
		} else if decision.Action == "blocked" {
			w.config.Log("character_permission_denied")
		} else {
			w.config.Log("character_silent")
		}
		return
	}
	if w.generation == nil && len(decision.Messages) == 0 && (decision.ImageID != "" || len(decision.MediaIDs) > 0) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.replyDelay(character)):
		}
	}
	if decision.QuoteReply {
		input.Directed = true
	}
	if input.Surf && w.config.SurfAllowed != nil && !w.config.SurfAllowed(input.Thread) {
		w.activity(Record{Event: "character_decision", Category: "decision", ThreadID: input.Thread, MessageID: input.TraceID, Reason: "surf_share_permission_changed", Status: "blocked", Origin: "surf"})
		return
	}
	if err := character.Claim(input); err != nil {
		w.config.Log("character_budget_limited")
		w.activity(Record{Event: "character_decision", Category: "decision", ThreadID: input.Thread, MessageID: input.TraceID, Reason: "reply_budget_or_quiet_hours", Status: "blocked"})
		return
	}
	if w.generation != nil && decision.ImageID != "" && character.ImageAllowed(decision.ImageID) {
		if _, ok := w.sender.(mediaNative); ok {
			if len(decision.Messages) > 0 {
				if !w.deferMediaReply(input, decision.Messages, len(decision.MediaIDs)+1) {
					return
				}
				decision.Messages = nil
			}
			w.queueMedia(ctx, character, input, companion.GeneratedMedia{ID: decision.ImageID, SourceID: decision.ImageID, Kind: "image", Title: "管理员授权图片"})
			decision.ImageID = ""
		}
	}
	if w.queueUtterancePlan(character, input, decision) {
		return
	}
	var image imageSender
	if decision.ImageID != "" {
		var ok bool
		image, ok = w.sender.(imageSender)
		if !ok || !character.ImageAllowed(decision.ImageID) {
			w.config.Log("image_permission_or_backend_unavailable")
			return
		}
	}
	if len(decision.MediaIDs) > 0 {
		if _, ok := w.sender.(mediaSender); !ok {
			w.config.Log("media_backend_unavailable")
			return
		}
	}
	if len(decision.MediaIDs) > 0 && len(decision.Messages) > 0 && w.runtime != nil {
		if _, ok := w.sender.(mediaNative); ok {
			if !w.deferMediaReply(input, decision.Messages, len(decision.MediaIDs)) {
				return
			}
			decision.Messages = nil
		}
	}
	for bubble, body := range decision.Messages {
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.replyDelay(character)):
		}
		if input.Mode != "direct" && character.Config.Quiet(time.Now()) {
			w.config.Log("character_quiet_hours")
			return
		}
		text := strings.TrimSpace(body)
		if input.Surf && w.config.SurfAllowed != nil && !w.config.SurfAllowed(input.Thread) {
			return
		}
		if character.Config.ShowAILabel && !strings.HasPrefix(text, "[AI]") {
			text = "[AI] " + text
		}
		sendCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		_, err := w.sendText(sendCtx, input, bubble, text)
		cancel()
		if err != nil {
			status := "failed"
			if errors.Is(err, sending.ErrOutcomeUnknown) {
				status = "unknown"
			}
			w.activity(Record{Event: "send_failed", Category: "error", ThreadID: input.Thread, MessageID: input.TraceID, Content: text, Origin: "bot", Status: status, Reason: sending.FailureReason(err), SendID: sending.IntentID(err)})
			if errors.Is(err, sending.ErrOutcomeUnknown) {
				w.config.Log("reply_outcome_unknown_not_retried")
			} else {
				w.config.Log("reply_failed_not_retried")
			}
			return
		}
		w.config.Log("reply_server_accepted")
		w.activity(Record{Event: "message_sent", Category: "sent", ThreadID: input.Thread, MessageID: input.TraceID, Content: text, Origin: "bot", Status: "accepted"})
		if err := character.Accepted(input, text); err != nil {
			w.config.Log("memory_save_failed_after_accepted_reply")
			return
		}
		if character.Config.Permissions.Remember {
			w.config.Log("memory_saved")
		}
		if input.Mode == "proactive" {
			w.config.Log("proactive_reply_accepted")
		}
	}
	if decision.ImageID != "" {
		if input.Surf && w.config.SurfAllowed != nil && !w.config.SurfAllowed(input.Thread) {
			return
		}
		if ctx.Err() != nil || input.Mode != "direct" && character.Config.Quiet(time.Now()) {
			return
		}
		w.markSending(input.TraceID)
		if w.persistErr != nil {
			return
		}
		sendCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		_, err := image.SendImage(sendCtx, input.Thread, decision.ImageID)
		cancel()
		if err != nil {
			w.config.Log("image_send_failed_or_unknown_not_retried")
			status := "failed"
			if errors.Is(err, sending.ErrOutcomeUnknown) {
				status = "unknown"
			}
			w.activity(Record{Event: "send_failed", Category: "error", ThreadID: input.Thread, MessageID: input.TraceID, Content: "[图片] " + decision.ImageID, Status: status, Reason: sending.FailureReason(err), SendID: sending.IntentID(err), Origin: "bot"})
			return
		}
		if err := character.Accepted(input, "[发送了一张管理员授权的图片] "+decision.ImageID); err != nil {
			w.config.Log("memory_save_failed_after_accepted_reply")
		}
		w.config.Log("image_reply_server_accepted")
		w.activity(Record{Event: "message_sent", Category: "sent", ThreadID: input.Thread, MessageID: input.TraceID, Content: "[图片] " + decision.ImageID, Origin: "bot", Status: "accepted"})
	}
	for _, id := range decision.MediaIDs {
		if input.Surf && w.config.SurfAllowed != nil && !w.config.SurfAllowed(input.Thread) {
			return
		}
		if ctx.Err() != nil || input.Mode != "direct" && character.Config.Quiet(time.Now()) {
			return
		}
		media, ok := character.Media(id)
		if !ok {
			w.config.Log("media_permission_denied")
			return
		}
		if media.Kind == "voice" && rejectVoice(input.Text) {
			w.activity(Record{Event: "media_stopped", Category: "decision", ThreadID: input.Thread, MessageID: input.TraceID, Reason: "media_cancelled_by_user", Status: "stopped"})
			continue
		}
		if w.queueMedia(ctx, character, input, media) {
			continue
		}
		sourceID := media.SourceID
		if sourceID == "" {
			sourceID = media.ID
		}
		sender := w.sender.(mediaSender)
		w.markSending(input.TraceID)
		if w.persistErr != nil {
			return
		}
		// A voice upload can contain five paced notes (up to five minutes of
		// audio). Keep polling the same durable job throughout recording delays.
		sendTimeout := 2 * time.Minute
		if media.Kind == "voice" {
			sendTimeout = 12 * time.Minute
		}
		sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
		_, err := sender.SendMedia(sendCtx, input.Thread, media.Kind, sourceID)
		cancel()
		if err != nil {
			w.config.Log("generated_media_send_failed_or_unknown_not_retried")
			status := "failed"
			if errors.Is(err, sending.ErrOutcomeUnknown) {
				status = "unknown"
			}
			w.activity(Record{Event: "send_failed", Category: "error", ThreadID: input.Thread, MessageID: input.TraceID, Content: "[" + media.Kind + "] " + media.Title, Status: status, Reason: sending.FailureReason(err), SendID: sending.IntentID(err), Origin: "bot"})
			return
		}
		if err = character.Accepted(input, "[发送了 "+media.Kind+" 媒体]"); err != nil {
			w.config.Log("memory_save_failed_after_accepted_reply")
		}
		w.config.Log("generated_media_server_accepted")
		w.activity(Record{Event: "message_sent", Category: "sent", ThreadID: input.Thread, MessageID: input.TraceID, Content: "[" + media.Kind + "] " + media.Title, Origin: "bot", Status: "accepted"})
	}
}
func (w *Worker) tickCharacter(ctx context.Context) {
	if w.generation != nil && !w.generation.canGenerate() {
		return
	}
	if w.tickAgentTask(ctx) {
		return
	}
	if w.tickConversationWake(ctx) {
		return
	}
	if w.tickSurf(ctx) {
		return
	}
	character, ok := w.model.(*companion.Engine)
	if !ok || ctx.Err() != nil {
		return
	}
	if time.Since(w.lastMaintenance) >= time.Minute {
		if err := character.Store.Expire(character.Config, time.Now()); err != nil {
			w.config.Log("character_memory_maintenance_failed")
			return
		}
		w.lastMaintenance = time.Now()
	}
	for _, input := range character.Proactive(w.config.Channel, w.source.Account()) {
		if w.generation != nil && w.generation.jobs[input.Thread] != nil {
			continue
		}
		if !w.allowed[input.Thread] {
			continue
		}
		input.PersonaOverride = w.config.GroupRules[input.Thread].Persona
		input.TraceID = "proactive-" + time.Now().UTC().Format(time.RFC3339Nano)
		w.activity(Record{Event: "proactive_trigger", Category: "decision", ThreadID: input.Thread, MessageID: input.TraceID, Origin: "proactive", Status: "processing", Reason: "proactive_trigger"})
		if err := character.ClaimProactive(input); err != nil {
			w.config.Log("character_budget_limited")
			return
		}
		w.config.Log("proactive_claimed")
		w.respondCharacter(ctx, character, input)
		return
	}
}
