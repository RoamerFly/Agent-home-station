package bot

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/sending"
)

type reliableNative interface {
	Submit(context.Context, linuxnative.ToolSend) (linuxnative.ToolStatus, error)
	LookupSend(context.Context, string) (linuxnative.ToolStatus, error)
	ToolSendStatus(context.Context, string) (linuxnative.ToolStatus, error)
	Catalog(context.Context, string, string, string, string) (linuxnative.CatalogPage, error)
}
type delivery struct {
	ReviewEvidence   *companion.GenerationEvidence `json:"review_evidence,omitempty"`
	Trace            string                        `json:"trace"`
	Thread           string                        `json:"thread"`
	Text             string                        `json:"text"`
	Bubble           int                           `json:"bubble"`
	Key              string                        `json:"key"`
	ID               string                        `json:"id"`
	State            string                        `json:"state"`
	Attempts         int                           `json:"attempts"`
	Checks           int                           `json:"checks"`
	Created          time.Time                     `json:"created"`
	Next             time.Time                     `json:"next"`
	Quote            *linuxnative.ToolQuote        `json:"quote,omitempty"`
	Input            companion.Input               `json:"input"`
	Reason           string                        `json:"reason"`
	PreviousKeys     []string                      `json:"previous_keys,omitempty"`
	UncertainRounds  int                           `json:"uncertain_rounds,omitempty"`
	PreviousTexts    []string                      `json:"previous_texts,omitempty"`
	RetryReviewReady bool                          `json:"retry_review_ready,omitempty"`
	SubmittedText    string                        `json:"submitted_text,omitempty"`
}

func (w *Worker) markSending(trace string) { w.queuePhase(trace, "sending") }

func (w *Worker) sendText(ctx context.Context, input companion.Input, bubble int, text string) (sending.Receipt, error) {
	if w.surfOrigin(input) && w.config.SurfAllowed != nil && !w.config.SurfAllowed(input.Thread) {
		return sending.Receipt{}, errors.New("surf_share_permission_changed")
	}
	native, ok := w.sender.(reliableNative)
	if !ok || w.runtime == nil {
		var err error
		input, text, err = w.prepareGroundedText(ctx, input, text)
		if err != nil {
			return sending.Receipt{}, err
		}
		return w.sender.SendText(ctx, sending.TextRequest{Recipient: input.Thread, Text: text})
	}
	if len(w.runtime.Deliveries) >= 3000 {
		w.pruneRuntime()
		if len(w.runtime.Deliveries) >= 3000 {
			return sending.Receipt{}, errors.New("delivery queue full")
		}
	}
	for _, old := range w.runtime.Deliveries {
		if old.Trace == input.TraceID && old.Thread == input.Thread && old.Bubble == bubble {
			if old.State == "accepted" {
				return sending.Receipt{Recipient: old.Thread}, nil
			}
			return sending.Receipt{}, &sending.OutcomeError{Cause: sending.ErrOutcomeUnknown, Reason: "delivery_reconciliation_pending", IntentID: old.ID}
		}
	}
	var err error
	input, text, err = w.prepareGroundedText(ctx, input, text)
	if err != nil {
		return sending.Receipt{}, err
	}
	d := &delivery{Trace: input.TraceID, Thread: input.Thread, Text: text, Bubble: bubble, Created: time.Now().UTC(), State: "prepared", Input: input}
	d.Key = "bot-" + messageHash(input.Thread + "|" + input.TraceID + fmt.Sprint(bubble))[:48]
	if (w.config.Options.QuoteReplies && input.Directed || input.QuoteSharedMessage || input.ReplyContext != nil && input.ReplyContext.QuoteRequired) && input.QuoteServerID != "" && input.TraceID != "" {
		d.Quote = replyQuote(input)
	}
	w.runtime.Deliveries = append(w.runtime.Deliveries, d)
	if err := w.saveRuntime(); err != nil {
		return sending.Receipt{}, err
	}
	w.markSending(input.TraceID)
	if w.persistErr != nil {
		return sending.Receipt{}, w.persistErr
	}
	return w.submitDelivery(ctx, native, d, false)
}
func (w *Worker) submitDelivery(ctx context.Context, n reliableNative, d *delivery, lookupFirst bool) (sending.Receipt, error) {
	if d.ReviewEvidence != nil {
		ctx = companion.WithStoredGenerationEvidence(ctx, *d.ReviewEvidence)
	}
	status := linuxnative.ToolStatus{}
	var err error
	if lookupFirst {
		status, err = n.LookupSend(ctx, d.Key)
		if err != nil && !errors.Is(err, linuxnative.ErrToolNotFound) {
			return w.waitDelivery(d, "receipt_lookup_failed")
		}
	}
	if !lookupFirst || errors.Is(err, linuxnative.ErrToolNotFound) {
		in, text, validationErr := w.prepareGroundedText(ctx, d.Input, d.Text)
		if validationErr != nil {
			d.State, d.Reason = "stopped", "reply_grounding_invalid"
			w.persistErr = w.saveRuntime()
			return sending.Receipt{}, validationErr
		}
		d.Input, d.Text = in, text
		if gate := w.sendGate(d.Thread); time.Now().Before(gate) {
			d.State, d.Next, d.Reason = "prepared", gate, "delivery_pacing"
			w.persistErr = w.saveRuntime()
			return sending.Receipt{}, &sending.OutcomeError{Cause: sending.ErrOutcomeUnknown, Reason: "delivery_pacing"}
		}
		d.State = "submitting"
		d.SubmittedText = d.Text
		w.advanceSendGate(d.Thread)
		if e := w.saveRuntime(); e != nil {
			return sending.Receipt{}, e
		}
		status, err = n.Submit(ctx, linuxnative.ToolSend{ThreadID: d.Thread, Kind: "text", Text: d.Text, IdempotencyKey: d.Key, Quote: d.Quote})
		if err != nil {
			if reason := linuxnative.QuoteAdmissionReason(err); reason != "" {
				d.State, d.Reason = "stopped", reason
				w.persistErr = w.saveRuntime()
				return sending.Receipt{}, &sending.OutcomeError{Cause: sending.ErrRejected, Reason: reason}
			}
			return w.waitDelivery(d, "send_outcome_unknown")
		}
	}
	d.ID = status.ID
	for {
		d.State = status.State
		d.Reason = status.Reason
		if e := w.saveRuntime(); e != nil {
			return sending.Receipt{}, e
		}
		switch status.State {
		case "accepted":
			d.State = "accepted"
			if d.SubmittedText != "" {
				d.Text = d.SubmittedText
			}
			w.advanceSendGate(d.Thread)
			w.persistErr = w.saveRuntime()
			return sending.Receipt{Recipient: d.Thread, ServerMessageID: status.ServerID}, w.persistErr
		case "failed", "cancelled":
			if status.State == "failed" && status.Retryable {
				return w.waitDelivery(d, status.Reason)
			}
			d.State = "stopped"
			w.persistErr = w.saveRuntime()
			return sending.Receipt{}, &sending.OutcomeError{Cause: sending.ErrRejected, Reason: status.Reason, IntentID: d.ID}
		case "needs_review", "unknown":
			return w.waitDelivery(d, "submission_outcome_unknown")
		}
		select {
		case <-ctx.Done():
			return w.waitDelivery(d, "receipt_wait_timeout")
		case <-time.After(300 * time.Millisecond):
		}
		status, err = n.ToolSendStatus(ctx, d.ID)
		if err != nil {
			return w.waitDelivery(d, "receipt_lookup_failed")
		}
	}
}
func (w *Worker) waitDelivery(d *delivery, reason string) (sending.Receipt, error) {
	d.State = "waiting"
	d.Reason = reason
	d.UncertainRounds++
	d.RetryReviewReady = false
	d.Checks = 0
	d.Next = time.Now().Add(deliveryBackoff(w.config.Options.RetryDelaySeconds, d.UncertainRounds))
	if err := w.saveRuntime(); err != nil {
		w.persistErr = err
		return sending.Receipt{}, err
	}
	w.recordDeliveryWait(d, "delivery_random_backoff")
	return sending.Receipt{}, &sending.OutcomeError{Cause: sending.ErrOutcomeUnknown, Reason: "delivery_reconciliation_pending", IntentID: d.ID}
}

// Continuations have synthetic trace IDs. WeChat references must point to the
// original incoming message, not the ID of the background task that completed.
func replyQuote(in companion.Input) *linuxnative.ToolQuote {
	id := in.OriginTraceID
	if in.ReplyContext != nil && in.ReplyContext.AnchorMessageID != "" {
		id = in.ReplyContext.AnchorMessageID
	}
	if id == "" {
		id = in.TraceID
	}
	return &linuxnative.ToolQuote{MessageID: id, ServerID: in.QuoteServerID}
}

// Each uncertain submission doubles its wait, with jitter. At the ceiling the
// interval remains random (one to two hours); the durable job has no retry count
// or age expiry. Read failures never authorize a new send.
func deliveryBackoff(baseSeconds, round int) time.Duration {
	if baseSeconds < 30 {
		baseSeconds = 30
	}
	base := time.Duration(baseSeconds) * time.Second
	for i := 1; i < round && base < time.Hour; i++ {
		base *= 2
	}
	if base > time.Hour {
		base = time.Hour
	}
	return base + time.Duration(rand.Int64N(int64(base)+1))
}

// Shared by durable owner recovery tasks; no retry-count or age expiry.
func DeliveryRetryBackoff(round int) time.Duration { return deliveryBackoff(120, round) }

func (w *Worker) recordDeliveryWait(d *delivery, reason string) {
	w.activity(Record{Event: "delivery_waiting", Category: "decision", ThreadID: d.Thread, MessageID: d.Trace, Content: d.Text, SendID: d.ID, Reason: reason, Status: "waiting", RetryAttempts: d.Attempts, RetryAt: d.Next.Format(time.RFC3339)})
}
func (w *Worker) reconcileDeliveries(ctx context.Context) {
	n, ok := w.sender.(reliableNative)
	if !ok {
		return
	}
	for _, d := range w.runtime.Deliveries {
		if ctx.Err() != nil {
			return
		}
		if d.State == "accepted" || d.State == "stopped" || time.Now().Before(d.Next) {
			continue
		}
		if d.Bubble < 0 {
			readCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
			status, err := n.LookupSend(readCtx, d.Key)
			cancel()
			if err == nil && status.State == "accepted" {
				w.acceptReconciled(d)
				continue
			}
			found, complete, scanErr := w.observedDelivery(ctx, n, d)
			if found {
				w.acceptReconciled(d)
				continue
			}
			if err == nil && (status.State == "queued" || status.State == "sending" || status.State == "running") && time.Since(d.Created) < 5*time.Minute {
				d.Next = time.Now().Add(15 * time.Second)
				w.persistErr = w.saveRuntime()
				continue
			}
			if time.Since(d.Created) < 15*time.Minute && (time.Since(d.Created) < 5*time.Minute || scanErr != nil || !complete) {
				d.Next = time.Now().Add(30 * time.Second)
				w.persistErr = w.saveRuntime()
				continue
			}
			d.State = "stopped"
			w.activity(Record{Event: "tool_progress_receipt_unknown", Category: "decision", ThreadID: d.Thread, MessageID: d.Trace, Content: d.Text, Reason: "tool_progress_expired_no_late_resend", Status: "unknown", SendID: d.ID})
			w.persistErr = w.saveRuntime()
			continue
		}
		if d.State == "prepared" {
			superseded := false
			for _, p := range w.runtime.Plans {
				if p.Input.Thread == d.Thread && p.Input.TraceID == d.Trace && p.Slot == d.Bubble && len(p.Updates) > 0 {
					d.State = "stopped"
					d.Reason = "unsubmitted_text_needs_replan"
					p.Slot++
					p.State = "review_pending"
					superseded = true
					break
				}
			}
			if superseded {
				w.persistErr = w.saveRuntime()
				continue
			}
		}
		if !w.allowed[d.Thread] {
			d.State = "stopped"
			d.Reason = "conversation_not_allowed"
			w.activity(Record{Event: "delivery_stopped", Category: "decision", ThreadID: d.Thread, MessageID: d.Trace, Reason: "conversation_not_allowed", Status: "blocked", SendID: d.ID})
			w.persistErr = w.saveRuntime()
			continue
		}
		if c, ok := w.model.(*companion.Engine); ok && (!c.Config.Permissions.ReplyText || c.Config.Quiet(time.Now()) && d.Input.Mode != "direct") {
			continue
		}
		// Any earlier attempt may receive a late receipt. Never create a fresh
		// intent until every previous key has been checked successfully.
		historyClear := true
		for _, key := range d.PreviousKeys {
			readCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
			old, e := n.LookupSend(readCtx, key)
			cancel()
			if e == nil && old.State == "accepted" {
				d.ID = old.ID
				for i, oldKey := range d.PreviousKeys {
					if oldKey == key && i < len(d.PreviousTexts) {
						d.Text = d.PreviousTexts[i]
						break
					}
				}
				w.acceptReconciled(d)
				historyClear = false
				break
			}
			if e != nil && !errors.Is(e, linuxnative.ErrToolNotFound) || e == nil && (old.State == "queued" || old.State == "sending" || old.State == "submitting" || old.State == "running") {
				historyClear = false
				break
			}
		}
		if !historyClear {
			if d.State != "accepted" {
				d.Next = time.Now().Add(deliveryBackoff(w.config.Options.RetryDelaySeconds, d.UncertainRounds))
				w.persistErr = w.saveRuntime()
			}
			continue
		}
		if d.State == "prepared" || d.State == "submitting" {
			if d.Attempts > 0 && !w.config.Options.RetryUncertain {
				d.Next = time.Now().Add(time.Minute)
				w.persistErr = w.saveRuntime()
				continue
			}
			sendCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			_, err := w.submitDelivery(sendCtx, n, d, true)
			cancel()
			if err == nil {
				w.acceptReconciled(d)
			}
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		status, err := n.LookupSend(readCtx, d.Key)
		cancel()
		if err == nil && (status.State == "queued" || status.State == "sending" || status.State == "submitting" || status.State == "running") {
			d.Next = time.Now().Add(15 * time.Second)
			w.persistErr = w.saveRuntime()
			continue
		}
		if err == nil && status.State == "accepted" {
			if d.SubmittedText != "" {
				d.Text = d.SubmittedText
			}
			w.acceptReconciled(d)
			continue
		}
		if err == nil && (status.State == "failed" && !status.Retryable || status.State == "cancelled") {
			d.State = "stopped"
			w.activity(Record{Event: "send_failed", Category: "error", ThreadID: d.Thread, MessageID: d.Trace, Content: d.Text, Reason: status.Reason, Status: "failed", SendID: d.ID})
			w.persistErr = w.saveRuntime()
			continue
		}
		if err == nil && status.State == "failed" && status.Retryable {
			d.Reason = status.Reason
		}
		// Healthy catalog scans are mandatory; an unavailable read is never
		// treated as evidence of absence. Search one thread, never other chats.
		found, complete, scanErr := w.observedDelivery(ctx, n, d)
		if found {
			w.acceptReconciled(d)
			continue
		}
		if err != nil && !errors.Is(err, linuxnative.ErrToolNotFound) || scanErr != nil || !complete || !w.sender.Ready() {
			d.Next = time.Now().Add(30 * time.Second)
			w.persistErr = w.saveRuntime()
			continue
		}
		if !w.config.Options.RetryUncertain {
			d.Next = time.Now().Add(time.Minute)
			d.Reason = "delivery_retry_disabled"
			w.persistErr = w.saveRuntime()
			continue
		}
		d.Checks++
		d.Next = time.Now().Add(15 * time.Second)
		if w.persistErr = w.saveRuntime(); w.persistErr != nil {
			return
		}
		if d.Checks < 2 {
			continue
		}
		if !w.taskActive(d.Input) {
			d.State, d.Reason = "stopped", "task_cancelled_before_delivery"
			w.persistErr = w.saveRuntime()
			continue
		}
		if w.surfOrigin(d.Input) && w.config.SurfAllowed != nil && !w.config.SurfAllowed(d.Thread) {
			d.State, d.Reason = "stopped", "surf_share_permission_changed"
			w.persistErr = w.saveRuntime()
			w.activity(Record{Event: "delivery_stopped", Category: "decision", ThreadID: d.Thread, MessageID: d.Trace, Reason: d.Reason, Status: "stopped", Origin: "surf"})
			continue
		}
		if !d.RetryReviewReady {
			if c, ok := w.model.(*companion.Engine); ok {
				w.reviewUncertainDelivery(ctx, c, d)
				continue // Read the inbox and late receipts again before submitting.
			}
		}
		// Absence in local memory is not proof of non-delivery. The owner's
		// explicit retry policy permits another intent after two healthy scans.
		d.Attempts++
		d.Checks = 0
		{
			base := d.Key
			if len(d.PreviousKeys) > 0 {
				base = d.PreviousKeys[0]
			}
			d.PreviousKeys = append(d.PreviousKeys, d.Key)
			oldText := d.SubmittedText
			if oldText == "" {
				oldText = d.Text
			}
			d.PreviousTexts = append(d.PreviousTexts, oldText)
			d.Key = fmt.Sprintf("%s-retry%d", base, d.Attempts)
		}
		d.ID = ""
		d.SubmittedText = ""
		d.State = "prepared"
		if w.persistErr = w.saveRuntime(); w.persistErr != nil {
			return
		}
		w.activity(Record{Event: "delivery_retry", Category: "decision", ThreadID: d.Thread, MessageID: d.Trace, Content: d.Text, Reason: "delayed_retry_after_two_sync_checks", Status: "retrying", SendID: d.ID, RetryAttempts: d.Attempts})
		sendCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		_, err = w.submitDelivery(sendCtx, n, d, true)
		cancel()
		if err == nil {
			w.acceptReconciled(d)
		}
	}
}
func (w *Worker) acceptReconciled(d *delivery) {
	d.State = "accepted"
	w.advanceSendGate(d.Thread)
	if w.persistErr = w.saveRuntime(); w.persistErr != nil {
		return
	}
	event := "message_sent"
	if d.Bubble < 0 {
		event = "message_progress"
	}
	w.activity(Record{Event: event, Category: "sent", ThreadID: d.Thread, MessageID: d.Trace, Content: d.Text, Reason: "delivery_confirmed_by_receipt_or_sync", Status: "accepted", SendID: d.ID, Origin: "bot"})
	if c, ok := w.model.(*companion.Engine); ok {
		for _, p := range w.runtime.Plans {
			if d.Bubble >= 0 && p.Input.Thread == d.Thread && p.Input.TraceID == d.Trace {
				return
			}
		}
		var err error
		if d.Bubble < 0 {
			err = c.Store.AcceptedDelivery(d.Input, d.Text, "progress-"+d.Trace, c.Config, time.Now())
		} else {
			err = c.Accepted(d.Input, d.Text)
		}
		if err != nil {
			w.config.Log("memory_save_failed_after_accepted_reply")
		}
	}
}
func (w *Worker) observedDelivery(ctx context.Context, n reliableNative, d *delivery) (bool, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cursor := ""
	for i := 0; i < 10; i++ {
		p, e := n.Catalog(ctx, "messages", d.Thread, "", cursor)
		if e != nil {
			return false, false, e
		}
		older := false
		for _, m := range p.Items {
			at, e := time.Parse(time.RFC3339, mString(m, "createdAt"))
			if e != nil {
				continue
			}
			if at.Before(d.Created.Add(-5 * time.Second)) {
				older = true
				continue
			}
			body := mString(m, "body")
			matched := body == d.Text || d.SubmittedText != "" && body == d.SubmittedText
			for _, text := range d.PreviousTexts {
				matched = matched || body == text
			}
			if mString(m, "direction") == "outgoing" && matched && mString(m, "serverId") != "" {
				d.Text = body
				return true, true, nil
			}
		}
		if p.NextCursor == nil || *p.NextCursor == "" || older {
			return false, true, nil
		}
		cursor = *p.NextCursor
	}
	return false, false, nil
}
func mString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}
