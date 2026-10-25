package bot

import (
	"context"
	"errors"
	"fmt"
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
	Trace    string                 `json:"trace"`
	Thread   string                 `json:"thread"`
	Text     string                 `json:"text"`
	Bubble   int                    `json:"bubble"`
	Key      string                 `json:"key"`
	ID       string                 `json:"id"`
	State    string                 `json:"state"`
	Attempts int                    `json:"attempts"`
	Checks   int                    `json:"checks"`
	Created  time.Time              `json:"created"`
	Next     time.Time              `json:"next"`
	Quote    *linuxnative.ToolQuote `json:"quote,omitempty"`
	Input    companion.Input        `json:"input"`
	Reason   string                 `json:"reason"`
}

func (w *Worker) markSending(trace string) { w.queuePhase(trace, "sending") }

func (w *Worker) sendText(ctx context.Context, input companion.Input, bubble int, text string) (sending.Receipt, error) {
	native, ok := w.sender.(reliableNative)
	if !ok || w.runtime == nil {
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
	d := &delivery{Trace: input.TraceID, Thread: input.Thread, Text: text, Bubble: bubble, Created: time.Now().UTC(), State: "prepared", Input: input}
	d.Key = "bot-" + messageHash(input.Thread + "|" + input.TraceID + fmt.Sprint(bubble))[:48]
	if (w.config.Options.QuoteReplies && input.Directed || input.QuoteSharedMessage) && input.QuoteServerID != "" && input.TraceID != "" {
		d.Quote = &linuxnative.ToolQuote{MessageID: input.TraceID, ServerID: input.QuoteServerID}
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
	status := linuxnative.ToolStatus{}
	var err error
	if lookupFirst {
		status, err = n.LookupSend(ctx, d.Key)
		if err != nil && !errors.Is(err, linuxnative.ErrToolNotFound) {
			return w.waitDelivery(d, "receipt_lookup_failed")
		}
	}
	if !lookupFirst || errors.Is(err, linuxnative.ErrToolNotFound) {
		d.State = "submitting"
		if e := w.saveRuntime(); e != nil {
			return sending.Receipt{}, e
		}
		status, err = n.Submit(ctx, linuxnative.ToolSend{ThreadID: d.Thread, Kind: "text", Text: d.Text, IdempotencyKey: d.Key, Quote: d.Quote})
		if err != nil {
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
			w.persistErr = w.saveRuntime()
			return sending.Receipt{Recipient: d.Thread, ServerMessageID: status.ServerID}, w.persistErr
		case "failed", "cancelled":
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
	d.Next = time.Now().Add(time.Duration(w.config.Options.RetryDelaySeconds) * time.Second)
	if err := w.saveRuntime(); err != nil {
		w.persistErr = err
		return sending.Receipt{}, err
	}
	w.activity(Record{Event: "delivery_waiting", Category: "decision", ThreadID: d.Thread, MessageID: d.Trace, Content: d.Text, SendID: d.ID, Reason: "delivery_reconciliation_pending", Status: "waiting"})
	return sending.Receipt{}, &sending.OutcomeError{Cause: sending.ErrOutcomeUnknown, Reason: "delivery_reconciliation_pending", IntentID: d.ID}
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
		if d.State == "prepared" || d.State == "submitting" {
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
			w.acceptReconciled(d)
			continue
		}
		if err == nil && (status.State == "failed" || status.State == "cancelled") {
			d.State = "stopped"
			w.activity(Record{Event: "send_failed", Category: "error", ThreadID: d.Thread, MessageID: d.Trace, Content: d.Text, Reason: status.Reason, Status: "failed", SendID: d.ID})
			w.persistErr = w.saveRuntime()
			continue
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
		if time.Since(d.Created) > 24*time.Hour || !w.config.Options.RetryUncertain || d.Attempts >= 1 {
			d.State = "stopped"
			w.activity(Record{Event: "delivery_stopped", Category: "error", ThreadID: d.Thread, MessageID: d.Trace, Content: d.Text, Reason: "delivery_retry_limit_or_disabled", Status: "needs_review", SendID: d.ID})
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
		// No synced message is not proof of non-delivery. The owner's explicit
		// bounded resend policy permits exactly one fresh intent after two scans.
		d.Attempts = 1
		if !errors.Is(err, linuxnative.ErrToolNotFound) {
			d.Key += "-retry1"
		}
		d.State = "prepared"
		if w.persistErr = w.saveRuntime(); w.persistErr != nil {
			return
		}
		w.activity(Record{Event: "delivery_retry", Category: "decision", ThreadID: d.Thread, MessageID: d.Trace, Content: d.Text, Reason: "delayed_retry_after_two_sync_checks", Status: "retrying", SendID: d.ID})
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
			if mString(m, "direction") == "outgoing" && mString(m, "body") == d.Text && mString(m, "serverId") != "" {
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
