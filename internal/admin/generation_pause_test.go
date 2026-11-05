package admin

import (
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

func TestGenerationPauseStopsOwnerRecoveryWithoutLosingRealEvidence(t *testing.T) {
	for _, kind := range []string{"music", "plugin", "process", "device", "owner"} {
		for _, before := range []string{"running", "waiting"} {
			t.Run(kind+"/"+before, func(t *testing.T) {
				f := setup(t)
				task := resultFixture(kind)
				task.State = before
				task.OwnerRequested = true
				task.ReportSourceThread = "synthetic-report@chatroom"
				task.Goal = "保留原合成任务目标"
				task.Actions = []taskAction{{Tool: "server_exec", State: "succeeded", Result: `{"exit_code":0,"output":"actual synthetic tool result"}`}}
				task.PendingOperations = []string{"existing-cross-operation"}
				task.Delivery = &bot.TaskDelivery{State: "partial", Expected: 1, AcceptedParts: 1, TotalParts: 2}
				if err := f.manager.agent.task(task); err != nil {
					t.Fatal(err)
				}
				f.manager.finishAgentTask(companion.Input{Account: task.Account, Thread: task.Thread, TaskID: task.ID}, "generation_paused")
				got, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
				if !ok || got.State != "needs_review" || got.Goal != task.Goal || len(got.Actions) != 1 || got.Actions[0].Result != task.Actions[0].Result || len(got.PendingOperations) != 1 || got.Delivery == nil || got.Delivery.AcceptedParts != 1 {
					t.Fatal("paused generation retried or discarded actual effects", got, ok)
				}
			})
		}
	}
}

func TestRetryableModelContinuationPersistsReadOnlyCheckpoint(t *testing.T) {
	f, p := trustedAgent(t)
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.mu.Unlock()
	settings := f.manager.agent.config()
	settings.Enabled, settings.TasksEnabled = true, true
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	task := agentTask{ID: "retry-model-task", Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, OwnerRequested: true, State: "running", Next: time.Now().Add(-time.Second), RequestedText: "读取后继续总结"}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	evidence := companion.GenerationEvidence{RecallEpoch: "synthetic-source-version"}
	f.manager.retryAgentTask(companion.Input{Account: task.Account, Thread: task.Thread, Sender: task.Sender, TaskID: task.ID, RecoveryToolResults: []companion.RecoveryToolResult{{Tool: "memory_search", CallKey: "same-call", Result: `{"items":["safe"]}`, ResultID: "retained", ReadOnly: true}}, RecoveryEvidence: &evidence}, time.Millisecond, "model_rate_limited")
	stored, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok || stored.State != "waiting" || len(stored.RecoveryTools) != 1 || stored.RecoveryEvidence == nil {
		t.Fatal("retry checkpoint was not durable", stored, ok)
	}
	in, err := f.manager.claimAgentTask(time.Now().Add(time.Second), func(string) bool { return false })
	if err != nil || in == nil || len(in.RecoveryToolResults) != 1 || in.RecoveryToolResults[0].CallKey != "same-call" || in.RecoveryEvidence == nil || in.RecoveryEvidence.RecallEpoch != evidence.RecallEpoch {
		t.Fatal("claimed retry lost source checkpoint", in, err)
	}
}

func TestLateGenerationPauseDoesNotReviveSettledOrCancelledTask(t *testing.T) {
	for _, state := range []string{"completed", "cancelled", "failed"} {
		t.Run(state, func(t *testing.T) {
			f := setup(t)
			task := resultFixture("owner")
			task.State = state
			if err := f.manager.agent.task(task); err != nil {
				t.Fatal(err)
			}
			f.manager.finishAgentTask(companion.Input{Account: task.Account, Thread: task.Thread, TaskID: task.ID}, "generation_paused")
			got, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
			if !ok || got.State != state {
				t.Fatal("late pause revived terminal task", got, ok)
			}
		})
	}
}
