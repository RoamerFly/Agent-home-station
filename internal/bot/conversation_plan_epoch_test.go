package bot

import (
	"context"
	"testing"
	"time"

	"wechat-bot/internal/companion"
)

func TestGroupPlanRechecksUnsentReplyAfterEvidenceEpochChanges(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"continue"}`}
	w, engine, native, in := planWorker(t, model)
	in.Thread, in.IsGroup, in.TraceID = "room@chatroom", true, "group-original"
	in.Source, in.ReplyContext = nil, nil
	original := fixtureMessage(in.TraceID, in.Thread, in.Text, "incoming")
	original.AccountID, original.SenderID, original.ServerID = in.Account, in.Sender, in.QuoteServerID
	bindInputSource(&in, original, engine)
	if err := engine.Store.Observe(in, engine.Config, time.Now()); err != nil {
		t.Fatal(err)
	}
	proof, err := engine.BindReplyEvidence(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.ReplyContext = &proof
	if !w.queueUtterancePlan(engine, in, companion.Decision{Action: "reply", Messages: []string{"原消息的回复"}, ReplyContexts: []companion.ReplyContext{proof}}) {
		t.Fatal("plan not queued")
	}
	later := in
	later.TraceID, later.Text, later.ReplyContext = "group-later", "新的群消息", nil
	later.Source = nil
	followup := fixtureMessage(later.TraceID, later.Thread, later.Text, "incoming")
	followup.AccountID, followup.SenderID = later.Account, later.Sender
	later.OriginalText = &followup.Body
	later.MessageTime = followup.CreatedAt
	bindInputSource(&later, followup, engine)
	if err := engine.Store.Observe(later, engine.Config, time.Now()); err != nil {
		t.Fatal(err)
	}
	source, ok := engine.Store.SourceForInput(later)
	if !ok {
		t.Fatal("followup source missing")
	}
	if _, err := engine.Store.SaveMemory(context.Background(), companion.MemoryEntry{
		Scope: companion.ScopeID(later.Channel, later.Account, later.Thread), Subject: later.Sender,
		Kind: "fact", Detail: later.Text, Sources: []companion.MemorySource{{EventID: source.EventID, Quote: later.Text}},
	}, 0); err != nil {
		t.Fatal(err)
	}
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	p := w.runtime.Plans[0]
	if p.State != "review_pending" || p.Index != 0 || len(p.Sent) != 0 || len(p.ReplyContexts) != 0 || p.ReviewEvidence != nil || len(native.submissions) != 0 || len(w.runtime.Deliveries) != 0 {
		t.Fatalf("unsent reply did not enter review without submission: state=%s index=%d sent=%d proof=%d deliveries=%d submissions=%d", p.State, p.Index, len(p.Sent), len(p.ReplyContexts), len(w.runtime.Deliveries), len(native.submissions))
	}
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if model.calls != 1 || p.State != "queued" || p.ReviewEvidence == nil {
		t.Fatalf("current-context review did not resume the original plan: calls=%d state=%s evidence=%v", model.calls, p.State, p.ReviewEvidence)
	}
}

func TestReceiptOnlyPlanDoesNotReportCompletedRound(t *testing.T) {
	w, engine, _, in := planWorker(t, &planReviewModel{})
	w.queueUtterancePlan(engine, in, companion.Decision{Action: "reply", Messages: []string{"待核对发送"}})
	p := w.runtime.Plans[0]
	p.Created = time.Now().Add(-31 * time.Minute)
	p.InboxChecked = true
	w.runtime.Deliveries = []*delivery{{Thread: in.Thread, Trace: in.TraceID, Bubble: p.Slot, State: "waiting", UncertainRounds: 1}}
	var records []Record
	w.config.Record = func(record Record) { records = append(records, record) }
	w.reconcileUtterancePlans(context.Background())
	if p.State != "stopped" || len(records) == 0 || records[len(records)-1].Event != "utterance_plan_stopped" || records[len(records)-1].Status != "waiting" {
		t.Fatalf("receipt-only reconciliation was reported as complete: plan=%s records=%+v", p.State, records)
	}
}

func TestPrivatePlanRechecksUnsentReplyAfterEvidenceEpochChanges(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"continue"}`}
	w, engine, native, in := planWorker(t, model)
	w.queueUtterancePlan(engine, in, companion.Decision{Action: "reply", Messages: []string{"原私聊回复"}, ReplyContexts: []companion.ReplyContext{*in.ReplyContext}})
	source, ok := engine.Store.SourceForInput(in)
	if !ok {
		t.Fatal("original source missing")
	}
	if _, err := engine.Store.SaveMemory(context.Background(), companion.MemoryEntry{
		Scope: companion.ScopeID(in.Channel, in.Account, in.Thread), Subject: in.Sender,
		Kind: "fact", Detail: in.Text, Sources: []companion.MemorySource{{EventID: source.EventID, Quote: in.Text}},
	}, 0); err != nil {
		t.Fatal(err)
	}
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	p := w.runtime.Plans[0]
	if p.State != "review_pending" || !p.EpochReplan || p.Index != 0 || len(native.submissions) != 0 {
		t.Fatalf("private unsent reply was sent or stopped instead of replanned: state=%s epoch_replan=%v submissions=%d", p.State, p.EpochReplan, len(native.submissions))
	}
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if model.calls != 1 || p.State != "queued" || p.EpochReplan || p.ReviewEvidence == nil {
		t.Fatalf("private replan did not create current evidence: calls=%d state=%s epoch_replan=%v evidence=%v", model.calls, p.State, p.EpochReplan, p.ReviewEvidence)
	}
}

func TestStoppedEpochPlanRecoversSameTraceAfterRestart(t *testing.T) {
	w, engine, native, in := planWorker(t, &planReviewModel{})
	w.queueUtterancePlan(engine, in, companion.Decision{Action: "reply", Messages: []string{"未发送文字"}, ReplyContexts: []companion.ReplyContext{*in.ReplyContext}})
	p := w.runtime.Plans[0]
	p.State, p.StopReason, p.InboxChecked = "stopped", "reply_evidence_epoch_changed", false
	w.reconcileUtterancePlans(context.Background())
	if p.State != "review_pending" || !p.EpochReplan || p.Trace != in.TraceID || p.Index != 0 || len(w.runtime.Plans) != 1 || len(native.submissions) != 0 {
		t.Fatalf("old stopped plan was not safely resumed: state=%s trace=%s index=%d plans=%d submissions=%d", p.State, p.Trace, p.Index, len(w.runtime.Plans), len(native.submissions))
	}
}
