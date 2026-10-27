package admin

import (
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/plugins"
)

func resultFixture(kind string) agentTask {
	t := agentTask{ID: "result-" + kind, Account: "test", Thread: "filehelper", Sender: "owner", State: "waiting", RequestedText: "synthetic request", OriginTrace: "incoming", QuoteServerID: "quoted", Runs: 40}
	switch kind {
	case "music":
		t.MusicJob = "music-job"
	case "plugin":
		t.PluginJob = &pluginJobState{Ticket: plugins.JobTicket{State: "completed"}}
	case "process":
		t.ProcessID = "process-job"
	case "device":
		t.DeviceTask = "device-job"
	}
	return t
}

func TestEveryWorkerUsesSameResultContextWithoutThreeRunQuota(t *testing.T) {
	for _, kind := range []string{"music", "plugin", "process", "device"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			task := resultFixture(kind)
			if e := f.manager.agent.task(task); e != nil {
				t.Fatal(e)
			}
			task = f.manager.agent.list("test", "filehelper")[0]
			in, e := f.manager.claimTaskResult(task, time.Now(), map[string]any{"state": "completed"}, "read actual output")
			if e != nil || in == nil || in.TaskID != task.ID || in.OriginTraceID != "incoming" || in.QuoteServerID != "quoted" || !strings.Contains(in.Text, kind+"_completion") || in.RequestedText != "synthetic request" {
				t.Fatal(in, e)
			}
			if got := f.manager.agent.list("test", "filehelper")[0]; got.Runs != 41 || got.State != "running" {
				t.Fatal(got)
			}
			// A stale claim must not create another model turn.
			if second, e := f.manager.claimTaskResult(task, time.Now(), nil, ""); e != nil || second != nil {
				t.Fatal(second, e)
			}
		})
	}
}

func TestLateWorkerResultCannotResurrectCancellation(t *testing.T) {
	for _, kind := range []string{"music", "plugin", "process", "device"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			f.manager.agent.task(resultFixture(kind))
			snapshot := f.manager.agent.list("test", "filehelper")[0]
			cancel := copyTask(snapshot)
			cancel.State = "cancelled"
			f.manager.agent.task(cancel)
			if in, e := f.manager.claimTaskResult(snapshot, time.Now(), nil, ""); e != nil || in != nil {
				t.Fatal(in, e)
			}
			if got := f.manager.agent.list("test", "filehelper")[0]; got.State != "cancelled" {
				t.Fatal(got)
			}
		})
	}
}

func TestUnifiedFinishRetriesOnlyUnpreparedResultsAndPreservesAcceptedDelivery(t *testing.T) {
	for _, kind := range []string{"music", "plugin", "process", "device"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			task := resultFixture(kind)
			task.State = "running"
			f.manager.agent.task(task)
			in := companion.Input{Account: task.Account, Thread: task.Thread, TaskID: task.ID, TraceID: "result-delivery"}
			f.manager.finishAgentTask(in, "")
			got := f.manager.agent.list("test", "filehelper")[0]
			if got.State != "waiting" || !got.Next.After(time.Now()) {
				t.Fatal(got)
			}
			got.State = "running"
			if kind == "music" {
				got.MusicPrepared = true
			}
			if kind == "plugin" {
				got.PluginJob.Prepared = true
			}
			f.manager.agent.task(got)
			f.manager.finishAgentTask(in, "reply")
			for _, status := range []bot.TaskDelivery{{Trace: in.TraceID, State: "partial", Expected: 1, AcceptedParts: 1, TotalParts: 2}, {Trace: in.TraceID, State: "accepted", Expected: 1, Accepted: 1, AcceptedParts: 2, TotalParts: 2}} {
				if e := f.manager.updateAgentDelivery(in, status); e != nil {
					t.Fatal(e)
				}
			}
			got = f.manager.agent.list("test", "filehelper")[0]
			if got.State != "completed" || got.Delivery.AcceptedParts != 2 {
				t.Fatal(got)
			}
		})
	}
}

func TestSnapshotKeepsNewActionsAndListDoesNotLeakPointers(t *testing.T) {
	f := setup(t)
	task := resultFixture("plugin")
	f.manager.agent.task(task)
	snapshot := f.manager.agent.list("test", "filehelper")[0]
	f.manager.agent.mu.Lock()
	current := f.manager.agent.data.Tasks[task.ID]
	current.Actions = append(current.Actions, taskAction{Tool: "server_edit", Result: "verified"})
	f.manager.agent.data.Tasks[task.ID] = current
	f.manager.agent.mu.Unlock()
	snapshot.State = "running"
	saved, e := f.manager.agent.updateSnapshot(snapshot)
	if e != nil || !saved {
		t.Fatal(saved, e)
	}
	rows := f.manager.agent.list("test", "filehelper")
	if len(rows[0].Actions) != 1 {
		t.Fatal("lost tool evidence")
	}
	rows[0].PluginJob.Ticket.State = "mutated"
	rows[0].Actions[0].Result = "mutated"
	got := f.manager.agent.list("test", "filehelper")[0]
	if got.PluginJob.Ticket.State != "completed" || got.Actions[0].Result != "verified" {
		t.Fatal("snapshot leaked mutable state", got)
	}
}

func TestExplicitResumeIsDistinctFromLateWorkerRevival(t *testing.T) {
	f := setup(t)
	task := resultFixture("process")
	task.State = "cancelled"
	f.manager.agent.task(task)
	snapshot := f.manager.agent.list("test", "filehelper")[0]
	snapshot.State = "pending"
	if saved, e := f.manager.agent.updateSnapshot(snapshot); e != nil || saved {
		t.Fatal("worker revived cancellation", saved, e)
	}
	if saved, e := f.manager.agent.transitionSnapshot(snapshot, true); e != nil || !saved {
		t.Fatal("explicit resume blocked", saved, e)
	}
}
