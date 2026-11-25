package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
	"wechat-bot/internal/agentrun"
)

var ErrWakePermission = errors.New("conversation wake permission denied")
var ErrWakeContext = errors.New("conversation wake context unavailable")

// ConversationSettings controls scheduling, never extra recipient/tool permissions.
type ConversationSettings struct {
	Enabled            bool `json:"enabled"`
	AggregateMS        int  `json:"aggregate_ms"`
	GroupAggregateMS   *int `json:"group_aggregate_ms,omitempty"`
	RecheckOnMessage   bool `json:"recheck_on_message"`
	AllowWake          bool `json:"allow_wake"`
	WakeMinSeconds     int  `json:"wake_min_seconds"`
	WakeMaxSeconds     int  `json:"wake_max_seconds"`
	TaskTimeoutSeconds int  `json:"task_timeout_seconds,omitempty"`
}

func ConversationDefaults() *ConversationSettings {
	return &ConversationSettings{Enabled: true, AggregateMS: 2000, RecheckOnMessage: true, AllowWake: false, WakeMinSeconds: 30, WakeMaxSeconds: 600}
}
func (c Config) ConversationConfig() ConversationSettings {
	if c.Conversation == nil {
		return *ConversationDefaults()
	}
	return *c.Conversation
}

// Online group turns have a separate collection window. Retain explicit legacy
// tuning (including zero) while upgrading the former two-second default.
func (c ConversationSettings) CollectionWindow(thread string) time.Duration {
	ms := c.AggregateMS
	if strings.HasSuffix(thread, "@chatroom") {
		if c.GroupAggregateMS != nil {
			ms = *c.GroupAggregateMS
		} else if ms == 2000 {
			ms = 30000
		}
	}
	return time.Duration(ms) * time.Millisecond
}

func validateConversation(c *ConversationSettings) error {
	if c == nil {
		return nil
	}
	if c.GroupAggregateMS != nil && (*c.GroupAggregateMS < 0 || int64(*c.GroupAggregateMS) > int64((time.Duration(1<<63-1))/time.Millisecond)) {
		return errors.New("群消息收集时长无效")
	}
	if c.AggregateMS < 0 || c.AggregateMS > 5000 || c.WakeMinSeconds < 1 || c.WakeMaxSeconds < c.WakeMinSeconds || int64(c.WakeMaxSeconds) > int64((time.Duration(1<<63-1))/time.Second) || c.TaskTimeoutSeconds < 0 || int64(c.TaskTimeoutSeconds) > int64((time.Duration(1<<63-1))/time.Second) {
		return errors.New("请检查消息聚合窗口与延时补充范围")
	}
	return nil
}

type PendingReview struct {
	generationEvidence *GenerationEvidence
	EvidenceMessageID  string   `json:"evidence_message_id,omitempty"`
	RecallSentIndices  []int    `json:"recall_sent_indices,omitempty"`
	Reason             string   `json:"reason,omitempty"`
	Action             string   `json:"action"`
	Messages           []string `json:"messages,omitempty"`
	DelaySeconds       int      `json:"delay_seconds,omitempty"`
}

func (s *Store) ClaimConversationWake(in Input, c Config, now time.Time) error {
	return s.scopeTransaction(ScopeID(in.Channel, in.Account, in.Thread), func(data *persisted) error {
		if !c.Enabled || !c.ConversationConfig().AllowWake || !c.Permissions.Proactive || !c.Permissions.ReplyText || !c.Permissions.Remember || c.Quiet(now) {
			return ErrWakePermission
		}
		selected := false
		for _, id := range c.ProactiveThreads {
			selected = selected || id == in.Thread
		}
		if !selected {
			return ErrWakePermission
		}
		id := ScopeID(in.Channel, in.Account, in.Thread)
		scope, ok := data.Scopes[id]
		if !ok || len(scope.Messages) == 0 || now.Sub(scope.LastInteraction) > time.Duration(c.MemoryDays)*24*time.Hour {
			return ErrWakeContext
		}
		day := localDay(c, now)
		if data.ProactiveDay != day {
			data.ProactiveDay = day
			data.ProactiveCount = 0
		}
		scope.ProactiveCount = proactiveToday(scope, c, now) + 1
		scope.ProactiveDay = day
		scope.LastProactive = now
		scope.NextProactiveAt = nextProactiveEvaluation(c.ProactiveRule(in.Thread), now)
		data.Scopes[id] = scope
		data.ProactiveCount++
		return nil
	})
}

// ReviewPending cannot execute tools or send. Only the unsent tail can change.
func (e *Engine) ReviewPending(ctx context.Context, in Input, sent, pending []string, updates []ConversationEvent) (PendingReview, error) {
	return e.reviewPending(ctx, in, sent, pending, updates, nil, false)
}

// ReviewInterruption reads an explicit correction without responding to it.
// New messages remain normal independent conversation inputs.
func (e *Engine) ReviewInterruption(ctx context.Context, in Input, sent, pending []string, updates []ConversationEvent, media []DeliveredAttachment) (PendingReview, error) {
	return e.reviewPendingWithMode(ctx, in, sent, pending, updates, nil, false, "interruption", media)
}

// Caption review includes verified attachment delivery, not just the original
// search request. The model may still rewrite, delay or cancel the unsent text.
type DeliveredAttachment struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	State string `json:"state"`
}

func (e *Engine) ReviewMediaCaption(ctx context.Context, in Input, sent, pending []string, updates []ConversationEvent, media []DeliveredAttachment) (PendingReview, error) {
	return e.reviewPending(ctx, in, sent, pending, updates, nil, false, media)
}

// Running requests are not the unsent tail of an obsolete text reply.
func (e *Engine) ReviewGeneration(ctx context.Context, in Input, pending []string, updates []ConversationEvent) (PendingReview, error) {
	return e.reviewPending(ctx, in, nil, pending, updates, nil, true)
}

// DeliveryReview carries actual delivery evidence. This call cannot use tools.
type DeliveryReview struct {
	Reason   string    `json:"reason"`
	Attempts int       `json:"retry_attempts"`
	Created  time.Time `json:"first_attempt_at"`
}

func (e *Engine) ReviewDelivery(ctx context.Context, in Input, sent []string, text string, delivery DeliveryReview) (PendingReview, error) {
	return e.reviewPending(ctx, in, sent, []string{text}, nil, &delivery, false)
}

func (e *Engine) reviewPending(ctx context.Context, in Input, sent, pending []string, updates []ConversationEvent, delivery *DeliveryReview, generation bool, caption ...[]DeliveredAttachment) (PendingReview, error) {
	return e.reviewPendingWithMode(ctx, in, sent, pending, updates, delivery, generation, "", caption...)
}

func (e *Engine) reviewPendingWithMode(ctx context.Context, in Input, sent, pending []string, updates []ConversationEvent, delivery *DeliveryReview, generation bool, mode string, caption ...[]DeliveredAttachment) (PendingReview, error) {
	// Persisted plans can be reviewed by the base Engine while other requests
	// are running. Trace-dependent authorization must belong to this plan, not
	// whichever conversation last used a shared mutable adapter.
	scoped, err := e.ForEvidenceInput(in)
	if err != nil {
		return PendingReview{}, err
	}
	return scoped.reviewPendingScoped(ctx, in, sent, pending, updates, delivery, generation, mode, caption...)
}

// ForEvidenceInput checks the original stamp with the original actor's current
// permissions. The shared engine itself never acquires a conversation identity.
func (e *Engine) ForEvidenceInput(in Input) (*Engine, error) {
	if _, mutable := e.Tools.(interface{ SetTrace(Input) }); !mutable {
		return e, nil
	}
	fork, err := e.Fork()
	if err != nil {
		return nil, err
	}
	traced, ok := fork.Tools.(interface{ SetTrace(Input) })
	if !ok {
		return nil, errors.New("isolated evidence trace unavailable")
	}
	traced.SetTrace(in)
	return fork, nil
}

func (e *Engine) reviewPendingScoped(ctx context.Context, in Input, sent, pending []string, updates []ConversationEvent, delivery *DeliveryReview, generation bool, mode string, caption ...[]DeliveredAttachment) (PendingReview, error) {
	if in.ReplyContext != nil {
		if err := e.ValidateReplyContext(ctx, in, *in.ReplyContext); err != nil {
			return PendingReview{}, err
		}
		if in.ReplyContext.EvidenceEpoch != "" || in.ReplyContext.EvidenceAuthorization != "" {
			original := GenerationEvidence{RecallEpoch: in.ReplyContext.EvidenceEpoch, Authorization: in.ReplyContext.EvidenceAuthorization}
			if previous, ok := GenerationEvidenceFromContext(ctx); ok && previous != original {
				return PendingReview{}, generationEvidenceChanged()
			}
			ctx = withGenerationEvidence(ctx, original)
		}
	}
	evidence, err := e.captureGenerationEvidence(ctx)
	if err != nil {
		return PendingReview{}, err
	}
	ctx = withGenerationEvidence(ctx, evidence)
	if err := e.validateGenerationInputSource(ctx, in); err != nil {
		return PendingReview{}, err
	}
	data := map[string]any{"kind": "review_unsent_message_plan", "original_request": e.safeCurrentEvidence(in), "original_requester_id": in.Sender, "already_sent": sent, "pending_messages": pending, "new_events": e.pendingEventEvidence(ctx, in, updates), "current_time": time.Now().In(location(e.Config.Timezone)).Format(time.RFC3339)}
	if mode == "interruption" {
		data["kind"] = "explicit_self_correction_check"
	}
	if generation {
		data["kind"] = "review_running_task"
	}
	if delivery != nil {
		data["kind"] = "review_uncertain_delivery"
		data["delivery_evidence"] = delivery
	}
	if len(caption) > 0 {
		data["kind"] = "review_delivered_media_caption"
		data["delivered_attachments"] = caption[0]
	}
	if mode != "interruption" {
		scope := e.conversationScope(in, e.Config, time.Now())
		data["relationship_notes"] = map[string]any{"evidence_kind": "legacy_unverified_notes", "items": scope.Notes}
		data["recent_conversation"] = e.contextTurns(scope, in)
		if scope.Preferences != "" {
			data["conversation_preferences"] = scope.Preferences
		}
	}
	encoded, _ := json.Marshal(data)
	system := e.policy(in) + "\n复核旧发言计划尚未发送的部分；新消息可补充、调整或取消它。只输出JSON {action:continue|cancel|replace|delay,messages:[完整短句],delay_seconds:整数}。只有原请求者明确叫停、改为相冲突要求或旧内容过时才cancel，并指出依据消息；媒体后的同人说明通常属于补充。无关聊天或催促不撤销原任务。已发送内容保持原样；不确定时continue。"
	if mode == "interruption" {
		system = e.policy(in) + "\n只读判断原请求者的纠错是否使旧回复确实错误、过时或被取消。仅处理原请求；新消息另行正常处理。只输出 JSON：{\"action\":\"continue|cancel\",\"evidence_message_id\":\"造成取消的新消息ID\",\"recall_sent_indices\":[已发送且确实错误的段落索引],\"reason\":\"简短依据\"}。原回复仍正确时 continue。需要停止未发部分或撤回错误已发段落时 cancel；已发段落正确则不要列入撤回。"
	}
	if delivery != nil {
		system += "\n发送回执待核对：结合已接受回执和目标会话记录决定沿用、改写、取消或稍后复核。已接受的附件直接复用；replace只返回一条短消息，不确定时delay。"
	}
	if in.IsGroup && mode != "interruption" {
		system += "\n群聊只发送自然的当前话题内容；纯内部诊断可取消该条配文，原任务仍继续。"
	}
	if generation {
		system = e.policy(in) + "\n判断original_request是否被new_events中的原请求者明确叫停或替代。只输出JSON {action:continue|cancel|delay,evidence_message_id:字符串,reason:简短依据,delay_seconds:整数}。同人媒体图注、催促和其他人聊天通常不撤销；取消时给依据消息ID。无法确认时continue。"
	}
	if len(caption) > 0 && mode != "interruption" {
		system += "\ndelivered_attachments已被微信接受，pending_messages是未发配文；附件已发不代表配文重复。按实际附件和新上下文自然介绍、改写或取消配文；取消给出reason。already_sent仅指已发文字。"
	}
	if err := e.ValidateGenerationEvidence(ctx, evidence); err != nil {
		return PendingReview{}, err
	}
	answer, err := e.model.Chat(e.modelRequestContext(ctx, evidence), system, []map[string]string{{"role": "user", "content": string(encoded)}})
	if err != nil {
		return PendingReview{}, err
	}
	if err := e.ValidateGenerationEvidence(ctx, evidence); err != nil {
		return PendingReview{}, err
	}
	var d PendingReview
	if len(answer) > 12<<10 || json.Unmarshal([]byte(strings.TrimSpace(answer)), &d) != nil {
		return d, errors.New("invalid pending-plan review")
	}
	switch d.Action {
	case "continue", "cancel", "delay":
	case "replace":
		if generation {
			return d, errors.New("running task review cannot replace a reply plan")
		}
		if len(d.Messages) < 1 {
			return d, errors.New("invalid replacement plan")
		}
	default:
		return d, errors.New("invalid pending-plan action")
	}
	if mode == "interruption" {
		if d.Action != "continue" && d.Action != "cancel" {
			return d, errors.New("invalid interruption action")
		}
		if d.Action == "cancel" {
			if d.EvidenceMessageID == "" && len(updates) == 1 {
				d.EvidenceMessageID = updates[0].MessageID
			}
			found := false
			for _, update := range updates {
				found = found || update.MessageID == d.EvidenceMessageID
			}
			if !found {
				return d, errors.New("interruption evidence not in new events")
			}
			for _, index := range d.RecallSentIndices {
				if index < 0 || index >= len(sent) {
					return d, errors.New("interruption recall index out of range")
				}
			}
		} else if len(d.RecallSentIndices) > 0 {
			return d, errors.New("interruption recall without cancellation")
		}
	}
	for _, s := range d.Messages {
		if strings.TrimSpace(s) == "" || len(s) > 1<<20 || !utf8.ValidString(s) {
			return d, errors.New("invalid pending-plan message")
		}
	}
	if d.DelaySeconds < 0 || d.DelaySeconds > 300 {
		return d, errors.New("invalid pending-plan delay")
	}
	if in.IsGroup && !generation && mode != "interruption" && d.Action != "cancel" {
		candidate := pending
		if d.Action == "replace" {
			candidate = d.Messages
		}
		if diagnosticDraft(candidate) {
			// Reviews run concurrently with other conversations. Give the
			// report route this plan's identity without mutating shared tools.
			fork, err := e.Fork()
			if err != nil {
				return PendingReview{}, err
			}
			if _, ok := fork.Tools.(GroupReplyPrivacy); !ok {
				return PendingReview{}, errors.New("private_diagnostic_route_unavailable")
			}
			if traced, ok := fork.Tools.(interface{ SetTrace(Input) }); ok {
				traced.SetTrace(in)
			}
			clean, err := fork.reviewReplyPrivacy(ctx, in, Decision{Action: "reply", Messages: candidate})
			if err != nil {
				return PendingReview{}, err
			}
			if len(clean.Messages) == 0 {
				d.Action, d.Messages, d.Reason = "cancel", nil, "private_diagnostic_only"
			} else {
				d.Messages = clean.Messages
				if d.Action != "delay" {
					d.Action = "replace"
				}
			}
		}
	}
	if err := e.ValidateGenerationEvidence(ctx, evidence); err != nil {
		return PendingReview{}, err
	}
	d.generationEvidence = &evidence
	return d, nil
}

// Pending updates come from the native scheduler, rather than model prose.
// Reuse the same source/literal/observation serializer used by live followups;
// preserve the native quote edge and task-relevance metadata independently.
func (e *Engine) pendingEventEvidence(ctx context.Context, in Input, updates []ConversationEvent) []map[string]any {
	base, _ := json.Marshal(map[string]any{"kind": "current_message", "channel": in.Channel, "account": in.Account, "thread": in.Thread, "speaker_id": in.Sender})
	events := make([]agentrun.Event, 0, len(updates))
	for _, update := range updates {
		kind := 0
		if update.Source != nil {
			kind = update.Source.MessageType
		}
		events = append(events, agentrun.Event{ID: update.MessageID, Thread: in.Thread, Sender: update.Sender, Text: update.Text, At: update.At, Kind: kind})
	}
	rows := e.followupEvidence([]map[string]string{{"role": "user", "content": string(base)}}, events)
	for i, row := range rows {
		update := updates[i]
		resolved := row["source_event_id"] != nil
		invalid := false
		if resolved && update.Source != nil {
			// followupEvidence's native fallback has only message/sender IDs.
			// A pending plan may carry the actual original text digest. Avoid
			// binding queued wording to a corrected text from the same message.
			ev, ok := e.replyReferenceEvent(ctx, in, update.Source)
			if !ok || (update.Source.MessageType == 0 || update.Source.MessageType == 1) && update.Source.BodyHash != "" && ev.Hash != update.Source.BodyHash {
				invalid = true
			}
		}
		row["new_request"] = update.NewRequest
		row["relation"] = update.Relation
		row["new_request_is_not_authorization"] = true
		if update.Source != nil && !resolved {
			row["source"] = update.Source
		}
		if update.ReplyTo != nil {
			row["quoted_source"] = update.ReplyTo
			row["quote_chain"] = quoteRoles(update.Sender, update.Source, update.ReplyTo)
			row["quoted_by_id"] = update.Sender
			row["quoted_author_id"] = update.ReplyTo.Sender
		}
		if resolved && !invalid {
			continue
		}
		if source := update.Source; source != nil {
			invalid = invalid || source.EventID != "" || source.Account != "" && source.Account != in.Account || source.Thread != "" && source.Thread != in.Thread || source.Sender != "" && source.Sender != update.Sender
			if (source.MessageType == 0 || source.MessageType == 1) && source.BodyHash != "" && source.BodyHash != sourceHash(update.Text) {
				invalid = true
			}
		}
		// An existing but unreadable/revised source must not re-enter through
		// the unpersisted native-message fallback after forgetting or collision.
		if e.Store != nil && e.Store.db != nil && update.MessageID != "" {
			var count int
			if err := e.Store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE scope=? AND trace=? AND role='user'", ScopeID(in.Channel, in.Account, in.Thread), update.MessageID).Scan(&count); err != nil || count > 0 {
				invalid = true
			}
		}
		if invalid {
			row["text"] = ""
			row["media_observations"] = nil
			row["user_text_state"] = "invalidated"
			row["evidence_kind"] = "unavailable_event_reference"
			delete(row, "source_event_id")
			if update.Source != nil {
				row["source"] = update.Source
				if update.Source.EventID != "" {
					row["source_event_id"] = update.Source.EventID
				}
			}
		} else if row["user_text_state"] == "session_original" {
			row["user_text_state"] = "native_pending_original"
		}
	}
	return rows
}

type ConversationEvent struct {
	Source     *SourceRef `json:"source,omitempty"`
	ReplyTo    *SourceRef `json:"reply_to,omitempty"`
	Relation   string     `json:"relation,omitempty"`
	SenderName string     `json:"sender_name,omitempty"`
	MessageID  string     `json:"message_id"`
	Sender     string     `json:"sender"`
	Text       string     `json:"text"`
	At         string     `json:"at"`
	NewRequest bool       `json:"new_request"`
	// MediaReadFailed is a host observation status, never retained user text.
	MediaReadFailed bool `json:"media_read_failed,omitempty"`
}
