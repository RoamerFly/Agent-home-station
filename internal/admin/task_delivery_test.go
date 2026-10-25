package admin

import (
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

func TestTaskWaitsForFullDeliveryAndRetainsUnknownOutcome(t *testing.T) {
	f := setup(t)
	task := agentTask{ID: "song-task", Account: "test", Thread: "filehelper", Sender: "owner", MusicJob: "job-one", MusicPrepared: true, State: "running"}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	in := companion.Input{Account: "test", Thread: "filehelper", TaskID: task.ID, TraceID: "delivery-one"}
	f.manager.finishAgentTask(in, "reply")
	if got := f.manager.agent.list("test", "filehelper")[0]; got.State != "waiting_delivery" {
		t.Fatal("queue handoff called complete", got)
	}
	d := bot.TaskDelivery{Trace: in.TraceID, State: "partial", Expected: 2, Accepted: 1, AcceptedParts: 1, TotalParts: 2}
	if err := f.manager.updateAgentDelivery(in, d); err != nil {
		t.Fatal(err)
	}
	if got := f.manager.agent.list("test", "filehelper")[0]; got.State != "waiting_delivery" || got.Delivery.Accepted != 1 {
		t.Fatal(got)
	}
	if _, err := f.manager.claimAgentTask(time.Now().Add(time.Minute), func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	d.State = "needs_review"
	if err := f.manager.updateAgentDelivery(in, d); err != nil {
		t.Fatal(err)
	}
	if got := f.manager.agent.list("test", "filehelper")[0]; got.State != "needs_review" {
		t.Fatal(got)
	}
	// Delayed verified receipts complete the same task without regeneration.
	d.State = "accepted"
	d.Accepted = 2
	d.AcceptedParts = 2
	if err := f.manager.updateAgentDelivery(in, d); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newAgentControl(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.list("test", "filehelper")[0]; got.State != "completed" || got.Delivery.Accepted != 2 {
		t.Fatal("delivery evidence lost on restart", got)
	}
	other := in
	other.Thread = "other@chatroom"
	d.State = "failed"
	if err := f.manager.updateAgentDelivery(other, d); err != nil {
		t.Fatal(err)
	}
	if f.manager.agent.list("test", "filehelper")[0].State != "completed" {
		t.Fatal("cross-thread mutation")
	}
}
