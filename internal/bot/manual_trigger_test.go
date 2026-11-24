package bot

import (
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestManualTriggerReprocessesOnlyUnsubmittedOriginal(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"silent"}`}
	w, c, native, in := planWorker(t, model)
	original := fixtureMessage(in.TraceID, in.Thread, in.Text, "incoming")
	original.SenderID = in.Sender
	w.source = &fakeSource{messages: map[string]linuxnative.Message{in.TraceID: original}}
	w.runtime.Inbox = append(w.runtime.Inbox, inboxItem{Event: fixtureEvent("1", in.TraceID, in.Thread, "incoming", "remote"), Phase: "stopped"})
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"旧草稿"}})
	w.runtime.Plans[0].State = "stopped"
	w.runtime.Plans[0].StopReason = "reply_evidence_epoch_changed"
	w.runtime.Plans[0].ReplyContexts = nil
	result, err := w.triggerRoundProcessing(t.Context(), in.Thread, in.TraceID)
	if err != nil || result.Status != "queued" || len(native.submissions) != 0 {
		t.Fatal("manual trigger failed or resent old draft", result, err, native.submissions)
	}
	for _, plan := range w.runtime.Plans {
		if plan != nil && plan.Input.TraceID == in.TraceID && len(plan.Messages) > 0 && plan.Messages[0] == "旧草稿" {
			t.Fatal("stale unsubmitted draft survived manual reprocessing")
		}
	}
}

func TestManualTriggerChecksUnknownReceiptWithoutResending(t *testing.T) {
	w, _, native, in := planWorker(t, &planReviewModel{answer: `{"action":"silent"}`})
	original := fixtureMessage(in.TraceID, in.Thread, in.Text, "incoming")
	original.SenderID = in.Sender
	w.source = &fakeSource{messages: map[string]linuxnative.Message{in.TraceID: original}}
	w.runtime.Inbox = append(w.runtime.Inbox, inboxItem{Event: fixtureEvent("1", in.TraceID, in.Thread, "incoming", "remote"), Phase: "stopped"})
	w.runtime.Deliveries = append(w.runtime.Deliveries, &delivery{Trace: in.TraceID, Thread: in.Thread, Input: in, Bubble: 0, Text: "旧回复", SubmittedText: "旧回复", State: "awaiting_evidence", Key: "original-key", ID: "original-job"})
	result, err := w.triggerRoundProcessing(t.Context(), in.Thread, in.TraceID)
	if err != nil || result.Status != "receipt_pending" || len(native.submissions) != 0 || w.runtime.Deliveries[0].Key != "original-key" {
		t.Fatal("manual trigger replayed an unknown native send", result, err, native.submissions)
	}
}
