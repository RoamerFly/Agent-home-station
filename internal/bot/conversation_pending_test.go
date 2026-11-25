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
	if got := w.pendingQueueParent(thread); got != "old" {
		t.Fatalf("queued message lost its active plan parent: %q", got)
	}
	w.runtime.Deliveries[0].State = "waiting"
	w.runtime.Plans = append(w.runtime.Plans, &utterancePlan{Input: companion.Input{Thread: thread, TraceID: "new"}, State: "review_pending"})
	if !w.pendingConversationThread(thread) {
		t.Fatal("a newer active plan was ignored")
	}
	if got := w.pendingQueueParent(thread); got != "new" {
		t.Fatalf("receipt-only plan became the queued message parent: %q", got)
	}
}

func TestPendingQueueParentUsesMediaInputTraceWhenLegacyTraceIsEmpty(t *testing.T) {
	const thread = "room@chatroom"
	in := companion.Input{Thread: thread, TraceID: "media-input"}
	w := &Worker{runtime: &runtimeState{Media: []*mediaDelivery{{Input: in, State: "running"}}}}
	if got := w.pendingQueueParent(thread); got != in.TraceID {
		t.Fatalf("media input trace was lost: %q", got)
	}
	w.runtime.Media = nil
	w.runtime.MediaReplies = []*mediaReply{{Input: in, State: "waiting_delivery"}}
	if got := w.pendingQueueParent(thread); got != in.TraceID {
		t.Fatalf("media reply input trace was lost: %q", got)
	}
}
