package bot

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/sending"
)

type utterancePlan struct {
	ReviewEvidence    *companion.GenerationEvidence `json:"review_evidence,omitempty"`
	EpochReplan       bool                          `json:"epoch_replan,omitempty"`
	ReviewReason      string                        `json:"review_reason,omitempty"`
	ReviewReturnState string                        `json:"review_return_state,omitempty"`
	ReviewedUpdateIDs []string                      `json:"reviewed_update_ids,omitempty"`
	PendingRecall     []int                         `json:"pending_recall,omitempty"`
	RecallNext        time.Time                     `json:"recall_next,omitempty"`
	RecallChecks      int                           `json:"recall_checks,omitempty"`
	WakePending       bool                          `json:"wake_pending,omitempty"`
	ReplyContexts     []companion.ReplyContext      `json:"reply_contexts,omitempty"`
	Paused            bool                          `json:"paused,omitempty"`
	WakeReason        string                        `json:"wake_reason,omitempty"`
	StopReason        string                        `json:"stop_reason,omitempty"`
	MediaCaption      bool                          `json:"media_caption,omitempty"`
	InboxChecked      bool                          `json:"inbox_checked"`
	Trace             string                        `json:"trace"`
	ID                string                        `json:"id"`
	Input             companion.Input               `json:"input"`
	Quote             *linuxnative.ToolQuote        `json:"quote,omitempty"`
	Messages          []string                      `json:"messages"`
	Sent              []string                      `json:"sent"`
	Index             int                           `json:"index"`
	Slot              int                           `json:"slot"`
	State             string                        `json:"state"`
	Next              time.Time                     `json:"next"`
	Created           time.Time                     `json:"created"`
	Updates           []companion.ConversationEvent `json:"updates,omitempty"`
	ReviewFailures    int                           `json:"review_failures,omitempty"`
	WakeAfterSeconds  int                           `json:"wake_after_seconds,omitempty"`
	WaitForReply      bool                          `json:"wait_for_reply,omitempty"`
}

func (p *utterancePlan) terminal() bool { return p.State == "accepted" || p.State == "stopped" }

func quotedOriginal(in companion.Input, m linuxnative.Message) bool {
	if m.ResolvedQuote == nil || m.ResolvedQuote.SenderID != in.Sender {
		return false
	}
	return m.ResolvedQuote.ID != "" && m.ResolvedQuote.ID == in.TraceID ||
		m.ResolvedQuote.ServerID != "" && in.Source != nil && m.ResolvedQuote.ServerID == in.Source.ServerID
}

// This only selects a read-only judgment. The model, not this text gate,
// decides whether an original plan must be cancelled.
func explicitCorrectionToPriorInput(in companion.Input, m linuxnative.Message) bool {
	if !messageAffectsInput(in, m) {
		return false
	}
	words := strings.ToLower(strings.TrimSpace(m.Body))
	if words == "" {
		return false
	}
	if _, after, ok := strings.Cut(words, "不是"); ok {
		// "是 konan 的声线，不是柯南" directly corrects the prior
		// request even without saying "刚才". Only the old requester's
		// matching term selects a read-only judgment; the model decides action.
		if target := strings.FieldsFunc(after, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }); len(target) > 0 &&
			utf8.RuneCountInString(target[0]) >= 2 && strings.Contains(strings.ToLower(in.Text+" "+in.RequestedText), target[0]) {
			return true
		}
	}
	verb := false
	for _, part := range []string{"说错", "发错", "写错", "不对", "错了", "更正", "纠正", "修改", "改成", "取消", "撤回", "别发", "不要发", "别做", "不要做", "别继续", "不用继续", "停止", "停下", "cancel", "retract", "wrong", "correct"} {
		verb = verb || strings.Contains(words, part)
	}
	if !verb {
		return false
	}
	if quotedOriginal(in, m) {
		return true
	}
	for _, part := range []string{"刚才", "刚刚", "之前", "前面", "上一条", "上条", "上一句", "我刚", "我发的", "我说的", "我之前", "那条消息", "那个请求", "原来那条", "earlier", "previous", "my last"} {
		if strings.Contains(words, part) {
			return true
		}
	}
	return false
}

func explicitCorrectionEvent(in companion.Input, event companion.ConversationEvent) bool {
	m := linuxnative.Message{ID: event.MessageID, SenderID: event.Sender, Body: event.Text}
	if event.ReplyTo != nil {
		m.ResolvedQuote = &linuxnative.Message{ID: event.ReplyTo.MessageID, ServerID: event.ReplyTo.ServerID, SenderID: event.ReplyTo.Sender}
	}
	return explicitCorrectionToPriorInput(in, m)
}

func (w *Worker) correctionPlan(m linuxnative.Message) *utterancePlan {
	var quoted, recent *utterancePlan
	for _, p := range w.runtime.Plans {
		if p.State == "stopped" || p.Input.Thread != m.ThreadID || !explicitCorrectionToPriorInput(p.Input, m) {
			continue
		}
		if quotedOriginal(p.Input, m) {
			if quoted == nil || p.Created.After(quoted.Created) {
				quoted = p
			}
			continue
		}
		if time.Since(p.Created) <= 30*time.Minute && (recent == nil || p.Created.After(recent.Created)) {
			recent = p
		}
	}
	if quoted != nil {
		return quoted
	}
	return recent
}

func (w *Worker) hasExplicitCorrectionTarget(m linuxnative.Message) bool {
	if w.runtime != nil && w.correctionPlan(m) != nil {
		return true
	}
	if w.generation != nil {
		if job := w.generation.jobs[m.ThreadID]; job != nil {
			return explicitCorrectionToPriorInput(job.input, m)
		}
	}
	return false
}

func (p *utterancePlan) hasUpdate(id string) bool {
	for _, event := range p.Updates {
		if event.MessageID == id {
			return true
		}
	}
	for _, reviewed := range p.ReviewedUpdateIDs {
		if reviewed == id {
			return true
		}
	}
	return false
}
func (p *utterancePlan) finishUpdateCheck() {
	for _, event := range p.Updates {
		if event.MessageID == "" {
			continue
		}
		found := false
		for _, reviewed := range p.ReviewedUpdateIDs {
			found = found || reviewed == event.MessageID
		}
		if !found {
			p.ReviewedUpdateIDs = append(p.ReviewedUpdateIDs, event.MessageID)
		}
	}
	if len(p.ReviewedUpdateIDs) > 32 {
		p.ReviewedUpdateIDs = p.ReviewedUpdateIDs[len(p.ReviewedUpdateIDs)-32:]
	}
	p.Updates = nil
}
func (p *utterancePlan) retainExplicitCorrections() {
	kept := p.Updates[:0]
	for _, event := range p.Updates {
		if explicitCorrectionEvent(p.Input, event) {
			kept = append(kept, event)
			continue
		}
		if event.MessageID != "" {
			p.ReviewedUpdateIDs = append(p.ReviewedUpdateIDs, event.MessageID)
		}
	}
	p.Updates = kept
	if len(p.ReviewedUpdateIDs) > 32 {
		p.ReviewedUpdateIDs = p.ReviewedUpdateIDs[len(p.ReviewedUpdateIDs)-32:]
	}
}

func (w *Worker) pendingConversationThread(thread string) bool {
	if w.generation != nil && w.generation.jobs[thread] != nil {
		return true
	}
	for _, p := range w.runtime.Plans {
		if p.Input.Thread == thread && !p.terminal() && !(p.MediaCaption && w.mediaTraceOnlyUnknown(p.Input.TraceID)) {
			// Receipt-only reconciliation keeps the original send intent alive,
			// but it must not occupy the conversation for every later task.
			if d := w.planDelivery(p); d != nil && (d.State == "waiting" && d.UncertainRounds > 0 || d.State == "awaiting_evidence") {
				continue
			}
			return true
		}
	}
	for _, d := range w.runtime.Media {
		if d.Input.Thread == thread && !d.terminal() && !awaitingMediaReceipt(d) {
			return true
		}
	}
	for _, r := range w.runtime.MediaReplies {
		if r.Input.Thread == thread && !r.terminal() && !w.mediaTraceOnlyUnknown(r.Trace) {
			return true
		}
	}
	return false
}
func (w *Worker) queueUtterancePlan(c *companion.Engine, in companion.Input, d companion.Decision) bool {
	return w.queueUtteranceFrom(c, in, d, 0, false, nil)
}
func (w *Worker) queueUtteranceFrom(c *companion.Engine, in companion.Input, d companion.Decision, start int, mediaCaption bool, updates []companion.ConversationEvent) bool {
	if w.runtime == nil || !c.Config.ConversationConfig().Enabled || len(d.Messages) == 0 || d.ImageID != "" || len(d.MediaIDs) > 0 {
		return false
	}
	for _, p := range w.runtime.Plans {
		if p.Input.Thread == in.Thread && p.Input.TraceID == in.TraceID {
			return true
		}
	}
	if len(w.runtime.Plans) >= 1000 {
		kept := w.runtime.Plans[:0]
		for _, p := range w.runtime.Plans {
			if !p.terminal() || time.Since(p.Created) < 24*time.Hour {
				kept = append(kept, p)
			}
		}
		w.runtime.Plans = kept
	}
	if len(w.runtime.Plans) >= 1000 {
		w.persistErr = errors.New("utterance plan queue full")
		return true
	}
	now := time.Now().UTC()
	p := &utterancePlan{WakeReason: d.WakeReason, ID: "utterance-" + messageHash(in.Thread + "|" + in.TraceID)[:32], Input: in, Messages: append([]string{}, d.Messages...), Sent: []string{}, State: "queued", Next: now.Add(w.replyDelay(c)), Created: now, WakeAfterSeconds: d.WakeAfterSeconds, WaitForReply: d.WaitForReply}
	p.MediaCaption = mediaCaption
	p.ReplyContexts = append([]companion.ReplyContext(nil), d.ReplyContexts...)
	p.Index, p.Slot = start, start
	p.Sent = append([]string{}, d.Messages[:start]...)
	p.Updates = append([]companion.ConversationEvent(nil), updates...)
	p.retainExplicitCorrections()
	if len(p.Updates) > 0 {
		p.State, p.ReviewReason, p.ReviewReturnState = "review_pending", "new_message", "queued"
	}
	if in.QuoteServerID != "" && in.TraceID != "" {
		p.Quote = replyQuote(in)
	}
	p.Trace = in.TraceID
	w.runtime.Plans = append(w.runtime.Plans, p)
	w.persistErr = w.saveRuntime()
	if w.persistErr != nil {
		return true
	}
	w.queuePhase(in.TraceID, "sending")
	w.publishTaskDelivery(in.TraceID)
	w.activity(Record{Event: "utterance_plan_queued", Category: "decision", ThreadID: in.Thread, MessageID: in.TraceID, Status: "queued", Reason: "semantic_message_plan", Content: strings.Join(d.Messages, "\n")})
	return true
}
func (w *Worker) noticeConversationMessage(m linuxnative.Message, eligible bool) {
	c, ok := w.model.(*companion.Engine)
	if !ok || w.runtime == nil || !c.Config.ConversationConfig().Enabled {
		return
	}
	if !eligible && strings.HasSuffix(m.ThreadID, "@chatroom") && !c.Config.Permissions.ObserveGroups {
		return
	}
	w.noticeGenerationMessage(m, eligible)
	if m.Type != 1 && !m.IsQuotedText() {
		return
	}
	correction := w.correctionPlan(m)
	if c.Config.ConversationConfig().RecheckOnMessage {
		for _, r := range w.runtime.MediaReplies {
			if r.terminal() || r.Input.Thread != m.ThreadID || r.Trace == m.ID {
				continue
			}
			if !messageAffectsInput(r.Input, m) {
				if r.Input.Source != nil {
					r.Input.QuoteSharedMessage = true
				}
				continue
			}
			if !explicitCorrectionToPriorInput(r.Input, m) {
				continue
			}
			seen := false
			for _, e := range r.Updates {
				seen = seen || e.MessageID == m.ID
			}
			if seen {
				continue
			}
			r.Updates = append(r.Updates, conversationEvent(m, eligible))
			if len(r.Updates) > 8 {
				r.Updates = r.Updates[len(r.Updates)-8:]
			}
		}
	}
	for _, wake := range w.runtime.Wakes {
		if wake.State == "waiting" && wake.WaitForReply && wake.Input.Thread == m.ThreadID && wake.Parent != m.ID && (wake.Input.Sender == "" || wake.Input.Sender == m.SenderID) {
			wake.State = "stopped"
			w.activity(Record{Event: "conversation_wake_stopped", Category: "decision", ThreadID: m.ThreadID, MessageID: wake.Parent, Status: "stopped", Reason: "new_reply_before_wake"})
		}
	}
	for _, p := range w.runtime.Plans {
		if p.State == "stopped" || p.Input.Thread != m.ThreadID || p.Input.TraceID == m.ID {
			continue
		}
		if !messageAffectsInput(p.Input, m) {
			if p.Input.Source != nil {
				p.Input.QuoteSharedMessage = true
				if p.Input.QuoteServerID != "" {
					p.Quote = replyQuote(p.Input)
				}
			}
			continue
		}
		if !c.Config.ConversationConfig().RecheckOnMessage || p != correction {
			continue
		}
		if p.hasUpdate(m.ID) {
			continue
		}
		p.Updates = append(p.Updates, conversationEvent(m, eligible))
		if len(p.Updates) > 8 {
			p.Updates = p.Updates[len(p.Updates)-8:]
		}
		if p.State == "queued" || p.State == "accepted" {
			p.ReviewReturnState = p.State
			p.State, p.ReviewReason = "review_pending", "new_message"
			p.InboxChecked = true
			p.Next = time.Now()
			w.activity(Record{Event: "utterance_interruption_check_queued", Category: "decision", ThreadID: m.ThreadID, MessageID: p.Input.TraceID, Status: "waiting", Reason: "explicit_self_correction"})
		}
	}
	w.persistErr = w.saveRuntime()
}
func (w *Worker) planDelivery(p *utterancePlan) *delivery {
	for _, d := range w.runtime.Deliveries {
		if d.Thread == p.Input.Thread && d.Trace == p.Input.TraceID && d.Bubble == p.Slot {
			return d
		}
	}
	return nil
}

// Older versions stopped an unsent plan when a newer memory revision made its
// reply proof stale. Reuse its original trace and review the remaining text;
// only an intent with no submission or receipt may be retired this way.
func (w *Worker) recheckUnsentReply(p *utterancePlan) bool {
	if p.Index >= len(p.Messages) {
		return false
	}
	if d := w.planDelivery(p); d != nil {
		if d.ID != "" || d.ServerID != "" || d.SubmittedText != "" {
			return false
		}
		d.State, d.Reason = "stopped", "unsubmitted_text_needs_replan"
		p.Slot++ // Retire only this unsubmitted idempotency key.
	}
	p.ReplyContexts = append([]companion.ReplyContext(nil), p.ReplyContexts[:min(p.Index, len(p.ReplyContexts))]...)
	p.Input.ReplyContext = nil
	p.ReviewEvidence = nil
	p.EpochReplan = true
	p.State, p.StopReason = "review_pending", ""
	p.Next = time.Now()
	w.persistErr = w.saveRuntime()
	if w.persistErr != nil {
		return false
	}
	w.activity(Record{Event: "utterance_plan_paused", Category: "decision", ThreadID: p.Input.Thread, MessageID: p.Input.TraceID, Status: "waiting", Reason: "reply_evidence_epoch_changed"})
	return true
}
func (w *Worker) acceptPlanBubble(c *companion.Engine, p *utterancePlan, text string) {
	in := p.Input
	if p.Index < len(p.ReplyContexts) {
		rc := p.ReplyContexts[p.Index]
		in.ReplyContext = &rc
	}
	if d := w.planDelivery(p); d != nil && d.State == "accepted" && d.Input.ReplyContext != nil {
		in.ReplyContext = d.Input.ReplyContext
	}
	var err error
	memoryConflict := false
	_, native := w.sender.(reliableNative)
	if native {
		// A durable plan that went through the native sender must be tied to
		// its confirmed receipt.  Do not substitute a plan/index-derived key:
		// that can collide with imported history and is not proof of delivery.
		d := w.planDelivery(p)
		if d == nil || d.State != "accepted" || deliveryMemoryKey(d) == "" {
			err = companion.ErrDeliveryIdentityRequired
		} else if d.Text != text {
			err = companion.ErrDeliveryMemoryConflict
		} else {
			err = w.acceptTextMemoryAt(c, in, p.Slot, text, deliveryAcceptedTime(d, time.Now()))
		}
	} else {
		err = c.Accepted(in, text)
	}
	if native && errors.Is(err, companion.ErrDeliveryMemoryConflict) {
		// The native receipt is already accepted, but companion memory may
		// contain an older bubble for the same receipt (for example after a
		// historical replay).  Keep the durable delivery and the old memory;
		// never overwrite the remembered text and never stop the whole worker.
		memoryConflict = true
		err = nil
	}
	if err != nil {
		w.persistErr = err
		return
	}
	p.Sent = append(p.Sent, text)
	p.Index++
	p.Slot++
	p.Next = time.Now().Add(w.replyDelay(c))
	p.State = "queued"
	p.retainExplicitCorrections()
	if len(p.Updates) > 0 {
		p.State, p.ReviewReason, p.ReviewReturnState = "review_pending", "new_message", "queued"
	}
	if p.Index >= len(p.Messages) {
		if len(p.Updates) > 0 {
			p.ReviewReturnState = "accepted"
			p.WakePending = true
		} else {
			p.State = "accepted"
			w.scheduleConversationWake(c, p.Input, p.WakeAfterSeconds, p.WaitForReply, p.WakeReason)
		}
	}
	w.persistErr = w.saveRuntime()
	if w.persistErr != nil {
		return
	}
	reason := "semantic_plan_bubble_accepted"
	if p.MediaCaption {
		reason = "media_accepted_then_caption"
	}
	sendID := ""
	for _, d := range w.runtime.Deliveries {
		if d.Thread == p.Input.Thread && d.Trace == p.Input.TraceID && d.Bubble == p.Slot-1 {
			sendID = d.ID
			break
		}
	}
	w.activity(Record{Event: "message_sent", Category: "sent", ThreadID: p.Input.Thread, MessageID: p.Input.TraceID, Content: text, Status: "accepted", Origin: "bot", Reason: reason, SendID: sendID})
	if memoryConflict {
		w.activity(Record{Event: "delivery_memory_conflict", Category: "error", ThreadID: p.Input.Thread, MessageID: p.Input.TraceID, Content: text, Status: "accepted", Reason: "accepted_delivery_kept_memory_conflict", ToolCode: "delivery_memory_conflict", SendID: sendID, Origin: "bot"})
	}
	w.refreshMediaParent(p.Input.TraceID)
}
func (w *Worker) reconcileUtterancePlans(ctx context.Context) {
	if w.runtime == nil {
		return
	}
	for _, p := range w.runtime.Plans {
		if w.inputRoundClosed(p.Input) {
			continue
		}
		if len(p.PendingRecall) > 0 {
			w.reconcilePlanRecalls(ctx, p)
		}
	}
	c, ok := w.model.(*companion.Engine)
	if !ok {
		return
	}
	visited := map[string]bool{}
	for _, p := range w.runtime.Plans {
		if ctx.Err() != nil || w.persistErr != nil {
			return
		}
		if w.inputRoundClosed(p.Input) {
			continue
		}
		if p.State == "accepted" && p.WakePending {
			// A legacy ordinary-message review may have held the one-shot wake
			// after every text bubble was already accepted. Finish it without
			// consulting the model or revisiting the sent reply.
			w.scheduleConversationWake(c, p.Input, p.WakeAfterSeconds, p.WaitForReply, p.WakeReason)
			if w.persistErr != nil {
				return
			}
			p.WakePending = false
			w.persistErr = w.saveRuntime()
			if w.persistErr != nil {
				return
			}
		}
		if p.State == "stopped" && p.StopReason == "reply_evidence_epoch_changed" {
			w.recheckUnsentReply(p)
		}
		if p.State == "stopped" && p.StopReason == "correction_receipt_awaiting_evidence" {
			if d := w.planDelivery(p); d != nil && d.State == "accepted" {
				w.resumeCorrectionAfterReceipt(p, d)
			}
		}
		if p.Paused || p.terminal() || visited[p.Input.Thread] || !p.InboxChecked {
			continue
		}
		visited[p.Input.Thread] = true
		if p.State == "review_pending" && p.ReviewReason == "new_message" {
			d := w.planDelivery(p)
			if d == nil || d.State == "prepared" && d.ID == "" && d.SubmittedText == "" {
				if !w.draining() && !time.Now().Before(p.Next) {
					if w.generation != nil {
						w.startPlanReview(ctx, c, p)
					} else {
						reviewCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
						decision, err := w.reviewPlan(w.modelContext(reviewCtx, p.Input.Thread, p.Input.TraceID), c, p)
						cancel()
						w.applyPlanReview(p, decision, err)
					}
				}
				continue
			}
		}
		// An already submitted bubble is reconciled independently, never replaced
		// or assumed absent because a new human message arrived.
		if d := w.planDelivery(p); d != nil {
			// The delivery reconciler owns the persisted receipt deadline. Plans
			// only consume its outcome, even if their pacing timer expires first.
			if d.State == "accepted" {
				w.acceptPlanBubble(c, p, d.Text)
			} else if d.State == "stopped" {
				p.State = "stopped"
				w.persistErr = w.saveRuntime()
				w.refreshMediaParent(p.Input.TraceID)
			} else if d.State == "awaiting_evidence" {
				p.State, p.StopReason = "stopped", "receipt_only_reconciliation"
				if p.ReviewReason == "new_message" {
					p.StopReason = "correction_receipt_awaiting_evidence"
				}
				w.persistErr = w.saveRuntime()
				if w.persistErr != nil {
					return
				}
				w.activity(Record{Event: "utterance_plan_stopped", Category: "decision", ThreadID: p.Input.Thread, MessageID: p.Input.TraceID, Status: "waiting", Reason: p.StopReason})
			} else if d.State == "waiting" && d.UncertainRounds > 0 && time.Since(p.Created) > 30*time.Minute && p.ReviewReason != "new_message" {
				// The submitted text is now receipt-only work. Retire the old
				// conversation plan without changing the original send intent;
				// a late receipt is still reconciled by its original key.
				p.State, p.StopReason = "stopped", "receipt_only_reconciliation"
				w.persistErr = w.saveRuntime()
				if w.persistErr != nil {
					return
				}
				w.activity(Record{Event: "utterance_plan_stopped", Category: "decision", ThreadID: p.Input.Thread, MessageID: p.Input.TraceID, Status: "waiting", Reason: p.StopReason})
			} else {
				delete(visited, p.Input.Thread)
			}
			continue
		}
		if p.State == "waiting_receipt" {
			p.State = "stopped"
			w.persistErr = w.saveRuntime()
			w.activity(Record{Event: "utterance_plan_stopped", Category: "error", ThreadID: p.Input.Thread, MessageID: p.Input.TraceID, Status: "needs_review", Reason: "text_sender_cannot_reconcile_unknown"})
			continue
		}
		if !w.taskActive(p.Input) || !w.allowed[p.Input.Thread] || !c.Config.Enabled || !c.Config.Permissions.ReplyText || (!c.Config.ConversationConfig().Enabled) || p.Input.Surf && w.config.SurfAllowed != nil && !w.config.SurfAllowed(p.Input.Thread) {
			p.State = "stopped"
			w.persistErr = w.saveRuntime()
			w.activity(Record{Event: "utterance_plan_stopped", Category: "decision", ThreadID: p.Input.Thread, MessageID: p.Input.TraceID, Status: "stopped", Reason: "utterance_permission_or_expired"})
			w.refreshMediaParent(p.Input.TraceID)
			continue
		}
		if p.Input.Mode == "proactive" && c.Config.Quiet(time.Now()) {
			continue
		}
		if p.State == "review_pending" && !w.draining() && !time.Now().Before(p.Next) {
			if w.generation != nil {
				w.startPlanReview(ctx, c, p)
				continue
			}
			reviewCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			d, err := w.reviewPlan(w.modelContext(reviewCtx, p.Input.Thread, p.Input.TraceID), c, p)
			cancel()
			w.applyPlanReview(p, d, err)
			continue
		}
		if p.State == "review_pending" || time.Now().Before(p.Next) {
			continue
		}
		text := strings.TrimSpace(p.Messages[p.Index])
		in := p.Input
		if p.Index < len(p.ReplyContexts) {
			rc := p.ReplyContexts[p.Index]
			in.ReplyContext = &rc
		}
		if p.Quote != nil {
			in.QuoteServerID = p.Quote.ServerID
		}
		if w.beforeSubmit(ctx) != nil {
			return
		}
		p.State = "submitting"
		w.persistErr = w.saveRuntime()
		if w.persistErr != nil {
			return
		}
		sendCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if p.ReviewEvidence != nil {
			sendCtx = companion.WithStoredGenerationEvidence(sendCtx, *p.ReviewEvidence)
		}
		_, err := w.sendText(sendCtx, in, p.Slot, text)
		cancel()
		if err == nil {
			w.acceptPlanBubble(c, p, text)
		} else if errors.Is(err, errSubmissionDeferred) {
			p.State = "queued"
			w.persistErr = w.saveRuntime()
			return
		} else if errors.Is(err, sending.ErrOutcomeUnknown) {
			p.State = "waiting_receipt"
			p.Next = time.Now().Add(time.Second)
			w.persistErr = w.saveRuntime()
		} else if errors.Is(err, errReplyEvidenceEpochChanged) {
			// A new memory revision invalidated the unsent text's proof before
			// submission. The original reply can be reviewed with current context;
			// accepted bubbles and their receipt evidence remain untouched.
			if d := w.planDelivery(p); d != nil {
				if d.ID != "" || d.ServerID != "" || d.SubmittedText != "" {
					d.State, d.Next = "waiting", time.Now()
					p.State = "waiting_receipt"
					p.Next = time.Now().Add(time.Second)
					w.persistErr = w.saveRuntime()
					continue
				}
			}
			w.recheckUnsentReply(p)
		} else {
			p.State, p.StopReason = "stopped", sending.FailureReason(err)
			w.persistErr = w.saveRuntime()
			w.activity(Record{Event: "send_failed", Category: "error", ThreadID: in.Thread, MessageID: in.TraceID, Content: text, Status: "failed", Reason: sending.FailureReason(err)})
		}
	}
}

func (w *Worker) reconcilePlanRecalls(ctx context.Context, p *utterancePlan) {
	if ctx.Err() != nil || w.persistErr != nil || time.Now().Before(p.RecallNext) {
		return
	}
	remaining := make([]int, 0, len(p.PendingRecall))
	for _, index := range p.PendingRecall {
		if ctx.Err() != nil {
			remaining = append(remaining, index)
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		state, err := w.recallAcceptedPlanBubble(checkCtx, p, index)
		cancel()
		if state == "pending" && p.RecallChecks < 5 || state == "unavailable" && p.RecallChecks < 3 {
			remaining = append(remaining, index)
			continue
		}
		reason := "recall_check_finished"
		if err != nil {
			reason = "recall_check_error"
		}
		w.activity(Record{Event: "message_recall_status", Category: "decision", ThreadID: p.Input.Thread, MessageID: p.Input.TraceID, Status: state, Reason: reason})
	}
	p.PendingRecall = remaining
	p.RecallChecks++
	if len(remaining) > 0 {
		p.RecallNext = time.Now().Add(time.Minute)
	} else {
		p.RecallNext = time.Time{}
	}
	w.persistErr = w.saveRuntime()
}
