package admin

import (
	"testing"
	"time"

	"wechat-bot/internal/linuxnative"
)

func TestQueuedCrossMediaReceiptWaitsWithoutModelTurn(t *testing.T) {
	f, _, batch := crossRecoveryFixture(t, 1)
	n := &receiptLookupNative{forwardNative: f.manager.native.(*forwardNative), statuses: map[string]linuxnative.ToolStatus{}}
	f.manager.native = n
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.mu.Unlock()
	c := f.manager.agent.config()
	c.Enabled, c.TasksEnabled = true, true
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	task, ok := f.manager.agent.lookupTask(batch.TaskID, batch.Account, batch.Origin)
	if !ok || len(task.PendingOperations) != 1 {
		t.Fatal("missing original queued operation", ok, task.PendingOperations)
	}
	task.State = "waiting"
	task.OwnerRequested = true
	task.Checkpoint = "等待原视频转发回执"
	task.Next = now.Add(-time.Second)
	task.Runs = 1
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	claim := func(at time.Time) {
		t.Helper()
		in, err := f.manager.claimAgentTask(at, func(string) bool { return false })
		if err != nil || in != nil {
			t.Fatalf("unchanged queued receipt consumed a model turn: %+v, %v", in, err)
		}
	}
	claim(now)
	queued, _ := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if queued.Runs != 1 || queued.State != "waiting" || !queued.Next.After(now) || n.lookups != 1 {
		t.Fatalf("queued operation was not passively deferred: %+v, lookups=%d", queued, n.lookups)
	}
	claim(now)
	if n.lookups != 1 {
		t.Fatal("operation was polled again before its next check", n.lookups)
	}
	claim(queued.Next.Add(time.Second))
	queued, _ = f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if queued.Runs != 1 || n.lookups != 2 {
		t.Fatal("unchanged operation restarted the model", queued.Runs, n.lookups)
	}
	n.statuses[batch.Parts[0].Key] = acceptedCrossPart(batch, 0)
	in, err := f.manager.claimAgentTask(queued.Next.Add(time.Second), func(string) bool { return false })
	if err != nil || in == nil || in.TaskID != task.ID {
		observed, _ := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
		t.Fatalf("accepted receipt did not resume unfinished task: %+v, %v, task=%+v, lookups=%d", in, err, observed, n.lookups)
	}
	resumed, _ := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if resumed.Runs != 2 || len(resumed.PendingOperations) != 0 || n.submits.Load() != 0 {
		t.Fatal("receipt resume changed delivery or replayed send", resumed.Runs, resumed.PendingOperations, n.submits.Load())
	}
}

func TestFailedCrossMediaReceiptStillWakesAgent(t *testing.T) {
	f, _, batch := crossRecoveryFixture(t, 1)
	part := batch.Parts[0]
	n := &receiptLookupNative{
		forwardNative: f.manager.native.(*forwardNative),
		statuses: map[string]linuxnative.ToolStatus{
			part.Key: {State: "failed", ThreadID: batch.Target, SourceID: part.SourceID},
		},
	}
	f.manager.native = n
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.mu.Unlock()
	c := f.manager.agent.config()
	c.Enabled, c.TasksEnabled = true, true
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	task, _ := f.manager.agent.lookupTask(batch.TaskID, batch.Account, batch.Origin)
	task.State = "waiting"
	task.OwnerRequested = true
	task.Next = time.Now().Add(-time.Second)
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	in, err := f.manager.claimAgentTask(time.Now(), func(string) bool { return false })
	if err != nil || in == nil || in.TaskID != task.ID || n.submits.Load() != 0 {
		t.Fatalf("failed receipt was hidden or resent: %+v, %v", in, err)
	}
}
