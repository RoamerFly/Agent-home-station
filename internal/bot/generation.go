package bot

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

// Only the event loop owns Worker and runtimeState. Background jobs return
// immutable results or request a short, serialized state transition here.
type generationScheduler struct {
	wg            sync.WaitGroup
	ready         []generationResult
	readyReviews  []planReviewResult
	jobs          map[string]*generationJob
	reviews       map[string]context.CancelFunc
	reviewResults chan planReviewResult
	requests      chan generationRequest
	results       chan generationResult
}
type generationRequest struct {
	state *atomic.Int32
	ctx   context.Context
	run   func() error
	done  chan error
}
type generationJob struct {
	quietDeadline  time.Time
	reviewFailures int
	engine         *companion.Engine
	reviewed       string
	nextReview     time.Time
	cancelReason   string
	input          companion.Input
	cancel         context.CancelFunc
	cancelled      bool
	toolStarted    bool
	resourceKind   string
	updates        []companion.ConversationEvent
}
type generationResult struct {
	job                *generationJob
	engine             *companion.Engine
	input              companion.Input
	decision           companion.Decision
	err, processingErr error
	progressDelivered  bool
}

func (w *Worker) enableGenerations() {
	c, ok := w.model.(*companion.Engine)
	if !ok || !c.Config.ConversationConfig().Enabled {
		return
	}
	if c.Tools != nil {
		if _, ok := c.Tools.(interface{ ForkTools() companion.ToolAccess }); !ok {
			return
		}
	}
	w.generation = &generationScheduler{jobs: map[string]*generationJob{}, reviews: map[string]context.CancelFunc{}, reviewResults: make(chan planReviewResult, 4), requests: make(chan generationRequest, 64), results: make(chan generationResult, 4)}
}
func (w *Worker) closeGenerations() {
	if w.generation == nil {
		return
	}
	for _, j := range w.generation.jobs {
		j.cancel()
	}
	for _, cancel := range w.generation.reviews {
		cancel()
	}
	// Do not release the worker lock while an old task can still write memory
	// or a tool ledger that the replacement worker has already loaded.
	w.generation.wg.Wait()
}
func (s *generationScheduler) occupied() int     { return len(s.jobs) + len(s.reviews) }
func (s *generationScheduler) canGenerate() bool { return len(s.jobs) < 3 && s.occupied() < 4 }
func (s *generationScheduler) call(ctx context.Context, run func() error) error {
	r := generationRequest{ctx: ctx, run: run, done: make(chan error, 1), state: &atomic.Int32{}}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.requests <- r:
	}
	select {
	case <-ctx.Done():
		if r.state.CompareAndSwap(0, 2) {
			return ctx.Err()
		}
		// The actor already owns this closure. Wait for its bounded operation
		// before the caller reads values written by the closure.
		return <-r.done
	case err := <-r.done:
		return err
	}
}
func (w *Worker) runGenerationRequest(r generationRequest) {
	if !r.state.CompareAndSwap(0, 1) {
		r.done <- r.ctx.Err()
		return
	}
	if err := r.ctx.Err(); err != nil {
		r.done <- err
		return
	}
	r.done <- r.run()
}
func (w *Worker) drainGenerations(ctx context.Context) {
	if w.generation == nil {
		return
	}
	reviews := w.generation.readyReviews
	w.generation.readyReviews = nil
	for _, r := range reviews {
		w.finishPlanReview(r)
	}
	ready := w.generation.ready
	w.generation.ready = nil
	for _, r := range ready {
		w.finishGeneration(ctx, r)
	}
	for {
		select {
		case r := <-w.generation.requests:
			w.runGenerationRequest(r)
		case r := <-w.generation.results:
			w.finishGeneration(ctx, r)
		case r := <-w.generation.reviewResults:
			w.finishPlanReview(r)
		default:
			return
		}
		if w.persistErr != nil || ctx.Err() != nil {
			return
		}
	}
}

// Results collected while waiting are applied only after the next inbox scan.
// This prevents a newly completed download from submitting old media just
// ahead of an already-arrived stop/change-format request.
func (w *Worker) collectGenerations(ctx context.Context) {
	if w.generation == nil {
		return
	}
	for {
		select {
		case r := <-w.generation.requests:
			w.runGenerationRequest(r)
		case r := <-w.generation.results:
			w.generation.ready = append(w.generation.ready, r)
		case r := <-w.generation.reviewResults:
			w.generation.readyReviews = append(w.generation.readyReviews, r)
		default:
			return
		}
		if ctx.Err() != nil || w.persistErr != nil {
			return
		}
	}
}
func (w *Worker) waitGeneration(ctx context.Context, delay time.Duration) {
	if w.generation == nil {
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
		return
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	case r := <-w.generation.requests:
		w.runGenerationRequest(r)
	case r := <-w.generation.results:
		w.generation.ready = append(w.generation.ready, r)
	case r := <-w.generation.reviewResults:
		w.generation.readyReviews = append(w.generation.readyReviews, r)
	}
}
func (w *Worker) noticeGenerationMessage(m linuxnative.Message, eligible bool) {
	if w.generation == nil || m.Type != 1 && !m.IsQuotedText() {
		return
	}
	j := w.generation.jobs[m.ThreadID]
	if j == nil || j.input.TraceID == m.ID {
		return
	}
	for _, e := range j.updates {
		if e.MessageID == m.ID {
			return
		}
	}
	j.updates = append(j.updates, companion.ConversationEvent{MessageID: m.ID, Sender: m.SenderID, Text: m.Body, At: m.CreatedAt, NewRequest: eligible})
	if len(j.updates) > 8 {
		j.updates = j.updates[len(j.updates)-8:]
	}
}
func (w *Worker) deferBusyGeneration(ctx context.Context, m linuxnative.Message) bool {
	s := w.generation
	if j := s.jobs[m.ThreadID]; j != nil {
		// Explicit, verified stop/format changes may interrupt an in-flight voice
		// download as well as already queued notes. Unrelated chat does not kill it.
		if j.resourceKind == "voice" && rejectVoice(m.Body) && (w.addressed(m) || !strings.HasSuffix(m.ThreadID, "@chatroom")) {
			j.cancelled = true
			j.cancel()
		}
		w.queuePhase(m.ID, "received")
		return true
	}
	if !s.canGenerate() {
		w.queuePhase(m.ID, "received")
		return true
	}
	return false
}

func (w *Worker) startGeneration(ctx context.Context, c *companion.Engine, input companion.Input, analysis *linuxnative.Message, deadlineOverride ...time.Time) {
	s := w.generation
	if s.jobs[input.Thread] != nil || !s.canGenerate() {
		return
	}
	fork, err := c.Fork()
	if err != nil {
		w.modelFailed(input.Thread, input.TraceID, err)
		return
	}
	deadline := time.Now().Add(15 * time.Minute)
	quietDeadline := time.Time{}
	if input.Surf {
		if quiet := c.Config.QuietDeadline(time.Now()); !quiet.IsZero() && quiet.Before(deadline) {
			deadline = quiet
			quietDeadline = quiet
		}
		for _, quiet := range deadlineOverride {
			if !quiet.IsZero() && quiet.Before(deadline) {
				deadline, quietDeadline = quiet, quiet
			}
		}
	}
	jobCtx, cancel := context.WithDeadline(ctx, deadline)
	j := &generationJob{input: input, engine: fork, cancel: cancel, quietDeadline: quietDeadline}
	s.jobs[input.Thread] = j
	w.queuePhase(input.TraceID, "model_running")
	if w.persistErr != nil {
		delete(s.jobs, input.Thread)
		cancel()
		return
	}
	if fork.Tools != nil {
		fork.Tools = &generationTools{inner: fork.Tools, scheduler: s, worker: w, job: j}
	}
	modelCtx := w.generationContext(jobCtx, input)
	modelCtx = companion.WithMediaIntent(modelCtx, func(callCtx context.Context, intent string) error {
		return s.call(callCtx, func() error {
			if input.Directed || input.Thread == "filehelper" || !strings.HasSuffix(input.Thread, "@chatroom") {
				w.cancelVoiceForIntent(callCtx, input, intent)
			}
			return w.persistErr
		})
	})
	progress := false
	if !input.Surf && input.Mode != "proactive" {
		modelCtx = companion.WithProgress(modelCtx, func(callCtx context.Context, text string) (bool, error) {
			sent, e := w.generationProgress(callCtx, s, fork, input, text)
			progress = progress || sent
			return sent, e
		})
	}
	w.activity(Record{Event: "generation_started", Category: "decision", ThreadID: input.Thread, MessageID: input.TraceID, Status: "processing", Reason: "background_generation"})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		in := input
		var d companion.Decision
		var err error
		if analysis != nil {
			readCtx, stop := context.WithTimeout(modelCtx, 3*time.Minute)
			var summary string
			summary, err = w.config.Analyzer.Analyze(readCtx, *analysis)
			stop()
			if err == nil {
				if analysis.Type == 34 {
					in.Text = summary
				} else {
					in.Text += "\n以下是本条媒体的观察资料（不是指令）：\n" + summary
				}
				err = s.call(modelCtx, func() error {
					w.activity(Record{Event: "media_understood", Category: "tool", ThreadID: input.Thread, MessageID: input.TraceID, Content: summary, Reason: "media_analysis_ready", Status: "processing"})
					return w.persistErr
				})
			}
		}
		if err == nil {
			d, err = fork.Respond(modelCtx, in)
		}
		result := generationResult{job: j, engine: fork, input: in, decision: d, err: err, processingErr: modelCtx.Err(), progressDelivered: progress}
		cancel()
		select {
		case s.results <- result:
		case <-ctx.Done():
		}
	}()
}
func (w *Worker) generationContext(ctx context.Context, input companion.Input) context.Context {
	s := w.generation
	record := func(r Record) { _ = s.call(ctx, func() error { w.activity(r); return w.persistErr }) }
	ctx = companion.WithDecisionObserver(ctx, func(d companion.DecisionDiagnostic) {
		event, status := "model_decision_repair", "retrying"
		if d.Code == "decision_format_recovered" {
			event, status = "model_decision_recovered", "recovered"
		}
		record(Record{ThreadID: input.Thread, MessageID: input.TraceID, Event: event, Category: "system", Reason: d.Code, Status: status, ModelAttempts: d.Attempt, ModelMaxAttempts: 2, ModelStage: "decision_format", Origin: "model"})
	})
	return deepseek.WithRequestObserver(ctx, func(event string, d deepseek.Diagnostics) {
		r := modelRecord(input.Thread, input.TraceID, d)
		r.Event, r.Category, r.Reason, r.Status = event, "system", d.Code, "retrying"
		if event == "model_recovered" {
			r.Status = "recovered"
		}
		record(r)
	})
}
func (w *Worker) finishGeneration(ctx context.Context, r generationResult) {
	s := w.generation
	if s.jobs[r.input.Thread] != r.job {
		return
	}
	if !r.job.cancelled && len(r.job.updates) > 0 && r.engine.Config.ConversationConfig().RecheckOnMessage && r.job.reviewed != generationFingerprint(r.job) {
		s.ready = append(s.ready, r)
		return
	}
	if r.input.TaskID != "" && w.config.AgentTaskDone != nil {
		defer w.config.AgentTaskDone(r.input, r.decision.Action)
	}
	delete(s.jobs, r.input.Thread)
	if stop := s.reviews["generation-"+r.input.TraceID]; stop != nil {
		stop()
		delete(s.reviews, "generation-"+r.input.TraceID)
	}
	if !w.taskActive(r.input) {
		w.activity(Record{Event: "agent_task_cancelled", Category: "decision", ThreadID: r.input.Thread, MessageID: r.input.TraceID, Status: "stopped", Reason: "task_cancelled_before_delivery"})
		return
	}
	if r.input.Surf && !r.job.quietDeadline.IsZero() && !time.Now().Before(r.job.quietDeadline) {
		w.activity(Record{Event: "surf_review", Category: "tool", ThreadID: r.input.Thread, MessageID: r.input.TraceID, Origin: "surf", Status: "blocked", Reason: "quiet_hours"})
		w.queuePhase(r.input.TraceID, "blocked")
		return
	}
	for _, wake := range w.runtime.Wakes {
		trace := "wake-" + messageHash(wake.Parent + "|" + wake.Due.UTC().Format(time.RFC3339Nano))[:40]
		if wake.State == "running" && trace == r.input.TraceID {
			wake.State = "completed"
		}
	}
	if r.input.Preview {
		status, reason := "not_shared", "surf_observe_only_or_no_share_target"
		if r.err != nil {
			status, reason = "failed", "surf_model_failed"
		}
		w.activity(Record{Event: "surf_review", Category: "tool", MessageID: r.input.TraceID, Origin: "surf", Status: status, Reason: reason, Content: strings.Join(r.decision.Messages, "\n")})
		w.persistErr = w.saveRuntime()
		return
	}
	if r.job.cancelled {
		w.queuePhase(r.input.TraceID, "interrupted")
		reason := r.job.cancelReason
		if reason == "" {
			reason = "new_voice_instruction"
		}
		w.activity(Record{Event: "generation_stopped", Category: "decision", ThreadID: r.input.Thread, MessageID: r.input.TraceID, Status: "stopped", Reason: reason})
		return
	}
	for _, d := range w.runtime.Deliveries {
		if d.Trace == r.input.TraceID && d.Bubble < 0 && d.State == "accepted" {
			r.progressDelivered = true
		}
	}
	w.finishCharacter(ctx, r.engine, r.input, r.decision, r.err, r.processingErr, r.job.toolStarted, r.progressDelivered)
	if c := r.engine.Config.ConversationConfig(); c.RecheckOnMessage && len(r.job.updates) > 0 {
		for _, caption := range w.runtime.MediaReplies {
			if caption.Trace == r.input.TraceID && !caption.terminal() {
				caption.Updates = append([]companion.ConversationEvent(nil), r.job.updates...)
			}
		}
		for _, p := range w.runtime.Plans {
			if p.Trace == r.input.TraceID && !p.terminal() {
				p.Updates = append([]companion.ConversationEvent(nil), r.job.updates...)
				p.State = "review_pending"
			}
		}
	}
	for i := range w.runtime.Inbox {
		item := &w.runtime.Inbox[i]
		if item.Event.Data.MessageID == r.input.TraceID && (item.Phase == "model_running" || item.Phase == "tool_running") {
			w.queuePhase(r.input.TraceID, "completed")
			break
		}
	}
	w.persistErr = w.saveRuntime()
}

// Delegation preserves attachment/review capabilities while marking tool work
// durably before entering network/plugin code. No automatic effectful replay.
type generationTools struct {
	inner     companion.ToolAccess
	scheduler *generationScheduler
	worker    *Worker
	job       *generationJob
}

func (t *generationTools) List(ctx context.Context) ([]mcptools.Tool, error) {
	return t.inner.List(ctx)
}
func (t *generationTools) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if err := t.scheduler.call(ctx, func() error {
		t.job.toolStarted = true
		if name == "web_audio_search" {
			t.job.resourceKind = "voice"
		}
		if name == "web_media_download" || name == "media_reuse" {
			var a struct {
				Kind string `json:"kind"`
			}
			_ = json.Unmarshal(args, &a)
			t.job.resourceKind = a.Kind
		}
		t.worker.queuePhase(t.job.input.TraceID, "tool_running")
		return t.worker.persistErr
	}); err != nil {
		return "", err
	}
	return t.inner.Call(ctx, name, args)
}
func (t *generationTools) AgentContext(ctx context.Context) string {
	if x, ok := t.inner.(interface{ AgentContext(context.Context) string }); ok {
		return x.AgentContext(ctx)
	}
	return ""
}
func (t *generationTools) SetTrace(in companion.Input) {
	if x, ok := t.inner.(interface{ SetTrace(companion.Input) }); ok {
		x.SetTrace(in)
	}
}
func (t *generationTools) ResetMedia() {
	if x, ok := t.inner.(interface{ ResetMedia() }); ok {
		x.ResetMedia()
	}
}
func (t *generationTools) Media(id string) (companion.GeneratedMedia, bool) {
	if x, ok := t.inner.(interface {
		Media(string) (companion.GeneratedMedia, bool)
	}); ok {
		return x.Media(id)
	}
	return companion.GeneratedMedia{}, false
}
func (t *generationTools) ReviewSurf(ctx context.Context, d companion.Decision) (companion.Decision, error) {
	if x, ok := t.inner.(interface {
		ReviewSurf(context.Context, companion.Decision) (companion.Decision, error)
	}); ok {
		return x.ReviewSurf(ctx, d)
	}
	return d, nil
}

func (w *Worker) generationProgress(ctx context.Context, s *generationScheduler, c *companion.Engine, in companion.Input, text string) (bool, error) {
	allowed := false
	err := s.call(ctx, func() error {
		if j := s.jobs[in.Thread]; j != nil {
			j.toolStarted = true
		}
		w.queuePhase(in.TraceID, "tool_running")
		if w.persistErr != nil {
			return w.persistErr
		}
		if text == "" || w.config.Options == nil || !w.config.Options.AcknowledgeTools || !c.Config.Permissions.ReplyText || in.TaskID != "" {
			return nil
		}
		allowed = c.Claim(in) == nil
		return nil
	})
	if err != nil || !allowed {
		return false, err
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-time.After(w.replyDelay(c)):
	}
	body := text
	if c.Config.ShowAILabel && !strings.HasPrefix(body, "[AI]") {
		body = "[AI] " + body
	}
	sent := false
	pending := false
	err = s.call(ctx, func() error {
		// Only submission/brief receipt polling runs on the actor. The human
		// delay and all actual tool/model work remain outside the event loop.
		sendCtx, cancel := context.WithTimeout(ctx, time.Second)
		_, e := w.sendText(sendCtx, in, -1, body)
		cancel()
		w.queuePhase(in.TraceID, "tool_running")
		if w.persistErr != nil {
			return w.persistErr
		}
		if e != nil {
			for _, d := range w.runtime.Deliveries {
				if d.Trace == in.TraceID && d.Bubble < 0 && d.State != "stopped" {
					pending = true
				}
			}
			return nil
		}
		sent = true
		if e = c.Store.AcceptedDelivery(in, body, "progress-"+in.TraceID, c.Config, time.Now()); e != nil {
			return e
		}
		w.activity(Record{Event: "message_progress", Category: "sent", ThreadID: in.Thread, MessageID: in.TraceID, Content: body, Status: "accepted", Reason: "tool_progress_accepted", Origin: "bot"})
		return nil
	})
	if err != nil || !pending {
		return sent, err
	}
	deadline := time.Now().Add(44 * time.Second)
	for !sent && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		err = s.call(ctx, func() error {
			for _, d := range w.runtime.Deliveries {
				if d.Trace != in.TraceID || d.Bubble >= 0 {
					continue
				}
				if d.State == "accepted" {
					sent = true
					return nil
				}
				if d.State == "stopped" {
					pending = false
					return nil
				}
				n, ok := w.sender.(reliableNative)
				if !ok {
					pending = false
					return nil
				}
				readCtx, cancel := context.WithTimeout(ctx, time.Second)
				var status linuxnative.ToolStatus
				var e error
				if d.ID != "" {
					status, e = n.ToolSendStatus(readCtx, d.ID)
				} else {
					status, e = n.LookupSend(readCtx, d.Key)
				}
				cancel()
				if e == nil && status.State == "accepted" {
					w.acceptReconciled(d)
					sent = true
				}
				if e == nil && (status.State == "failed" || status.State == "cancelled") {
					d.State = "stopped"
					pending = false
					w.persistErr = w.saveRuntime()
				}
				return w.persistErr
			}
			pending = false
			return nil
		})
		if err != nil || !pending {
			break
		}
	}
	if !sent && err == nil {
		err = s.call(ctx, func() error {
			w.activity(Record{Event: "tool_progress_failed", Category: "error", ThreadID: in.Thread, MessageID: in.TraceID, Content: body, Status: "unknown", Reason: "progress_receipt_pending", Origin: "bot"})
			return w.persistErr
		})
	}
	return sent, err
}
