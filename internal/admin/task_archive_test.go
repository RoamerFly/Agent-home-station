package admin

import (
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

func TestConfirmedTasksLeaveActiveStorageButRetainScopedDeliveryEvidence(t *testing.T) {
	f := setup(t)
	task := agentTask{ID: "fixture-completed", Account: "test", Thread: "filehelper", Sender: "owner", State: "waiting_delivery", Updated: time.Now()}
	f.manager.agent.task(task)
	if err := f.manager.updateAgentDelivery(companion.Input{Account: "test", Thread: "filehelper", TaskID: task.ID}, bot.TaskDelivery{Trace: "same", State: "unknown", Expected: 1}); err != nil {
		t.Fatal(err)
	}
	if len(f.manager.agent.active("test", "")) != 1 {
		t.Fatal("unconfirmed task hidden")
	}
	if err := f.manager.updateAgentDelivery(companion.Input{Account: "test", Thread: "filehelper", TaskID: task.ID}, bot.TaskDelivery{Trace: "same", State: "accepted", Expected: 1, Accepted: 1}); err != nil {
		t.Fatal(err)
	}
	if len(f.manager.agent.active("test", "")) != 0 || len(f.manager.agent.data.Tasks) != 0 {
		t.Fatal("completed tasks still in active store")
	}
	history := f.manager.agent.taskHistory("test", "")
	if len(history) != 1 || history[0].Delivery.Accepted != 1 {
		t.Fatal("receipt evidence discarded")
	}
	reloaded, err := newAgentControl(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.active("test", "")) != 0 || len(reloaded.taskHistory("test", "")) != 1 || len(reloaded.taskHistory("other", "")) != 0 {
		t.Fatal("archive restart/scope failed")
	}
	if reloaded.task(agentTask{ID: task.ID, Account: "test", Thread: "filehelper", State: "waiting"}) == nil {
		t.Fatal("settled task silently reactivated")
	}
}

func TestAcknowledgementDoesNotCompleteWaitingBusinessGoal(t *testing.T) {
	f := setup(t)
	task := agentTask{ID: "still-working", Account: "test", Thread: "filehelper", State: "waiting", Checkpoint: "等待外部结果"}
	f.manager.agent.task(task)
	if err := f.manager.updateAgentDelivery(companion.Input{Account: "test", Thread: "filehelper", TaskID: task.ID}, bot.TaskDelivery{Trace: "ack", State: "accepted", TextExpected: 1, TextAccepted: 1}); err != nil {
		t.Fatal(err)
	}
	active := f.manager.agent.active("test", "")
	if len(active) != 1 || active[0].State != "waiting" {
		t.Fatal("a progress message completed the actual task")
	}
}

func TestSilentReportAndEmptyFollowupRetireWithoutOperatorOrDelivery(t *testing.T) {
	for _, report := range []bool{true, false} {
		f, p := trustedAgent(t)
		task := agentTask{ID: "original", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", ReportOnly: report, State: "running"}
		if report {
			task.ReportSourceThread = "fixture@chatroom"
		}
		if err := f.manager.agent.task(task); err != nil {
			t.Fatal(err)
		}
		in := p.trace
		in.TaskID = task.ID
		f.manager.finishAgentTask(in, "silent")
		history := f.manager.agent.taskHistory(task.Account, task.Thread)
		if len(f.manager.agent.active(task.Account, "")) != 0 || len(history) != 1 || history[0].State != "stopped" || history[0].Delivery != nil || history[0].WorkCompleted || f.manager.agentTaskActive(in) {
			t.Fatal(history)
		}
	}
}
