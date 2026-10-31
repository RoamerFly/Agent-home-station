package admin

import (
	"testing"
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
