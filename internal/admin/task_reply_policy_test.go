package admin

import (
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestScheduledExecutionWaitsSilentlyForResult(t *testing.T) {
	f, p, _ := memberReminder(t)
	task := agentTask{ID: "silent-task", Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, State: "running", Title: "synthetic execution", Checkpoint: "work", Next: time.Now()}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	in := taskResultInput(task, &companion.Input{TaskID: task.ID, Account: task.Account, Thread: task.Thread})
	if !in.SilentExecution {
		t.Fatal("execution not silent")
	}
	if f.manager.taskReplyReady(*in, companion.Decision{Action: "reply", Messages: []string{"arbitrary preparation text"}}) {
		t.Fatal("unfinished task spoke")
	}
	saved, _ := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if saved.State != "waiting" || !saved.Next.After(time.Now()) {
		t.Fatal("task was not continued")
	}
	saved.State = "completed"
	if err := f.manager.agent.task(saved); err != nil {
		t.Fatal(err)
	}
	if !f.manager.taskReplyReady(*in, companion.Decision{Action: "reply", Messages: []string{"result"}}) {
		t.Fatal("completed task result suppressed")
	}
	task.NotificationText = "synthetic notification"
	in = taskResultInput(task, &companion.Input{})
	if in.SilentExecution || in.NotificationText != task.NotificationText {
		t.Fatal("notification treated as execution")
	}
}
