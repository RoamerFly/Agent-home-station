package admin

import (
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestYieldedTaskKeepsExternalIdentityAndActionEvidence(t *testing.T) {
	f := setup(t)
	task := resultFixture("process")
	task.State = "running"
	task.Actions = []taskAction{{Tool: "server_exec", Result: "actual exit 0"}}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	f.manager.finishAgentTask(companion.Input{TaskID: task.ID, Account: task.Account, Thread: task.Thread}, "yielded_to_message")
	got := f.manager.agent.list(task.Account, task.Thread)[0]
	if got.State != "waiting" || got.ProcessID != task.ProcessID || len(got.Actions) != 1 || !got.Next.After(time.Now()) {
		t.Fatal(got)
	}
	// A stale completion cannot undo cancellation or a newer tool checkpoint.
	got.State = "cancelled"
	if err := f.manager.agent.task(got); err != nil {
		t.Fatal(err)
	}
	f.manager.finishAgentTask(companion.Input{TaskID: task.ID, Account: task.Account, Thread: task.Thread}, "yielded_to_message")
	if f.manager.agent.list(task.Account, task.Thread)[0].State != "cancelled" {
		t.Fatal("cancelled task revived")
	}
}

func TestRestartReconcilesKnownExternalJobsWithoutReplayingOperations(t *testing.T) {
	f := setup(t)
	for _, kind := range []string{"process", "music", "device", "unknown"} {
		task := resultFixture(kind)
		task.State = "running"
		if err := f.manager.agent.task(task); err != nil {
			t.Fatal(err)
		}
	}
	legacy := resultFixture("process")
	legacy.ID = "legacy-process"
	legacy.State = "needs_review"
	legacy.LastResult = "运行中断，需在管理页恢复；不会自动重放外部操作"
	f.manager.agent.task(legacy)
	cancelled := legacy
	cancelled.ID = "cancelled-process"
	cancelled.State = "cancelled"
	f.manager.agent.task(cancelled)
	a, err := newAgentControl(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range a.list("test", "filehelper") {
		want := "waiting"
		if task.ID == "result-unknown" {
			want = "needs_review"
		}
		if task.ID == "cancelled-process" {
			want = "cancelled"
		}
		if task.State != want {
			t.Fatal(task.ID, task.State, want)
		}
	}
	if len(a.data.Operations) != 0 {
		t.Fatal("restart created/replayed an operation")
	}
}
