package bot

import (
	"testing"
	"wechat-bot/internal/companion"
)

func TestReceiptOnlyTextPlanDoesNotBlockLaterConversation(t *testing.T) {
	const thread = "owner-thread"
	w := &Worker{runtime: &runtimeState{
		Plans:      []*utterancePlan{{Input: companion.Input{Thread: thread, TraceID: "old"}, State: "review_pending"}},
		Deliveries: []*delivery{{Thread: thread, Trace: "old", Bubble: 0, State: "waiting", UncertainRounds: 2}},
	}}
	if w.pendingConversationThread(thread) {
		t.Fatal("receipt-only lookup blocked a later task")
	}
	w.runtime.Deliveries[0].State = "prepared"
	if !w.pendingConversationThread(thread) {
		t.Fatal("unsubmitted plan did not reserve the conversation")
	}
	w.runtime.Deliveries[0].State = "waiting"
	w.runtime.Plans = append(w.runtime.Plans, &utterancePlan{Input: companion.Input{Thread: thread, TraceID: "new"}, State: "review_pending"})
	if !w.pendingConversationThread(thread) {
		t.Fatal("a newer active plan was ignored")
	}
}
