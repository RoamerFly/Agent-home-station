package admin

import (
	"testing"
	"time"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

func TestRepeatedGroupFailureStopsWithoutDiscardingProgress(t *testing.T) {
	f := setup(t)
	task := agentTask{ID: "repeated-group-error", Account: "test", Thread: "test@chatroom", Sender: "owner", State: "running", OriginTrace: "original-message", RequestedText: "读取分享", Checkpoint: "读取分享后处理"}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	in := companion.Input{Account: task.Account, Thread: task.Thread, TaskID: task.ID}
	for attempt := 1; attempt <= repeatedNoProgressLimit; attempt++ {
		if err := f.manager.finishAgentTask(in, ""); err != nil {
			t.Fatal(err)
		}
		got, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
		if !ok || got.NoProgressRuns != attempt {
			t.Fatalf("lost no-progress history after attempt %d: %+v", attempt, got)
		}
		if attempt == repeatedNoProgressLimit {
			if got.State != "stopped" || !got.Next.IsZero() || f.manager.agentTaskActive(in) {
				t.Fatalf("same failure continued to schedule work: %+v", got)
			}
			if len(f.manager.agent.active(task.Account, task.Thread)) != 0 {
				t.Fatal("stopped task remained in the active store")
			}
			break
		}
		if got.State != "waiting" || !got.Next.After(time.Now()) {
			t.Fatalf("first failed attempt was not retryable: %+v", got)
		}
		got.State = "running"
		got.Runs++
		if err := f.manager.agent.task(got); err != nil {
			t.Fatal(err)
		}
	}

	progress := agentTask{Checkpoint: "first", Goal: "task"}
	if progress.stopRepeatedNoProgress("turn:error") || progress.NoProgressRuns != 1 {
		t.Fatal("first observation stopped task")
	}
	progress.Checkpoint = "new verified result"
	progress.Runs++
	if progress.stopRepeatedNoProgress("turn:error") || progress.NoProgressRuns != 1 {
		t.Fatal("new checkpoint did not reset failure streak")
	}
	progress.ProcessID = "real-running-process"
	for i := 0; i < repeatedNoProgressLimit; i++ {
		if progress.stopRepeatedNoProgress("turn:error") {
			t.Fatal("live external job was stopped by the generic loop guard")
		}
	}
}

func TestRepeatedSavedCheckpointStopsWithoutCountingOneRunTwice(t *testing.T) {
	f := setup(t)
	task := agentTask{ID: "same-checkpoint", Account: "test", Thread: "test@chatroom", Sender: "owner", State: "running", OriginTrace: "request", Checkpoint: "稍后再检查", Runs: 1}
	in := companion.Input{Account: task.Account, Thread: task.Thread, TaskID: task.ID}
	for attempt := 1; attempt <= repeatedNoProgressLimit; attempt++ {
		task.State = "waiting"
		task.Next = time.Now().Add(time.Minute)
		if err := f.manager.agent.task(task); err != nil {
			t.Fatal(err)
		}
		if err := f.manager.finishAgentTask(in, "silent"); err != nil {
			t.Fatal(err)
		}
		if err := f.manager.finishAgentTask(in, "silent"); err != nil {
			t.Fatal(err)
		}
		got, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
		if !ok || got.NoProgressRuns != attempt {
			t.Fatalf("checkpoint attempt counted incorrectly: %+v", got)
		}
		if attempt == repeatedNoProgressLimit {
			if got.State != "stopped" || !got.Next.IsZero() {
				t.Fatalf("same saved checkpoint remained scheduled: %+v", got)
			}
			break
		}
		task = got
		task.State = "running"
		task.Runs++
	}
}

func TestRepeatedProviderFailureRetainsOriginalRecoveryCheckpoint(t *testing.T) {
	f := setup(t)
	task := agentTask{ID: "provider-retry", Account: "test", Thread: "test@chatroom", Sender: "owner", State: "running", OriginTrace: "request", RecoveryMessageID: "request", Checkpoint: "原请求", Runs: 1}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	in := companion.Input{Account: task.Account, Thread: task.Thread, TaskID: task.ID, RecoveryToolResults: []companion.RecoveryToolResult{{Tool: "web_fetch", CallKey: "same-read", Result: "saved result", ReadOnly: true}}}
	for attempt := 1; attempt <= repeatedNoProgressLimit; attempt++ {
		f.manager.retryAgentTask(in, time.Minute, "model_timeout")
		got, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
		if !ok || got.State != "waiting" || got.NoProgressRuns != 0 || got.RecoveryMessageID != "request" || len(got.RecoveryTools) != 1 || !got.Next.After(time.Now()) {
			t.Fatalf("provider retry lost the original task: %+v", got)
		}
		if attempt < repeatedNoProgressLimit {
			got.State = "running"
			got.Runs++
			if err := f.manager.agent.task(got); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCrossThreadParentWaitsWithoutModelAndSettlesAfterArchivedChild(t *testing.T) {
	f, _ := trustedAgent(t)
	cfg := f.manager.Config()
	cfg.Companion.Enabled = true
	f.manager.mu.Lock()
	f.manager.config = cfg.Config
	f.manager.mu.Unlock()
	now := time.Now()
	parent := agentTask{ID: "parent-request", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", State: "running", OwnerRequest: true, OriginTrace: "owner-message", Delivery: &bot.TaskDelivery{Trace: "ack", State: "checking_receipt", TextExpected: 1}}
	child := agentTask{ID: "cross-thread-child", ParentID: parent.ID, Account: parent.Account, Thread: "test@chatroom", Sender: parent.Sender, State: "waiting", Next: now.Add(time.Hour)}
	for _, task := range []agentTask{parent, child} {
		if err := f.manager.agent.task(task); err != nil {
			t.Fatal(err)
		}
	}
	in := companion.Input{Account: parent.Account, Thread: parent.Thread, TaskID: parent.ID}
	if !f.manager.taskReplyReady(in, companion.Decision{Action: "reply"}) {
		t.Fatal("child handoff suppressed the parent acknowledgement")
	}
	if err := f.manager.finishAgentTask(in, "reply"); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.updateAgentDelivery(in, bot.TaskDelivery{Trace: "ack", State: "accepted", TextExpected: 1, TextAccepted: 1}); err != nil {
		t.Fatal(err)
	}
	waiting, ok := f.manager.agent.lookupTask(parent.ID, parent.Account, parent.Thread)
	if !ok || waiting.State != "waiting" {
		t.Fatalf("parent did not retain active child ownership: %+v", waiting)
	}
	if claimed, err := f.manager.claimAgentTask(now, func(string) bool { return false }); err != nil || claimed != nil {
		t.Fatalf("waiting parent consumed a model turn: %+v, %v", claimed, err)
	}
	recovered, err := newAgentControl(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.agent = recovered
	if claimed, err := f.manager.claimAgentTask(now, func(string) bool { return false }); err != nil || claimed != nil {
		t.Fatalf("restart reclaimed the parent while its child was active: %+v, %v", claimed, err)
	}
	child.State = "completed"
	if err := f.manager.agent.task(child); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.agent.retireSettled(); err != nil {
		t.Fatal(err)
	}
	if claimed, err := f.manager.claimAgentTask(now, func(string) bool { return false }); err != nil || claimed != nil {
		t.Fatalf("settled child restarted the parent model: %+v, %v", claimed, err)
	}
	settled, ok := f.manager.agent.lookupTask(parent.ID, parent.Account, parent.Thread)
	if !ok || settled.State != "completed" || !settled.Next.IsZero() || settled.Runs != waiting.Runs {
		t.Fatalf("archived child did not settle parent: %+v", settled)
	}
}

func TestCrossThreadParentKeepsUnconfirmedReplyReceipt(t *testing.T) {
	parent := agentTask{State: "waiting", Delivery: &bot.TaskDelivery{State: "checking_receipt", Expected: 1}}
	settleCrossThreadParent(&parent, crossThreadChildState{found: true, id: "finished-child"})
	if parent.State != "waiting_delivery" || !parent.WorkCompleted || parent.Delivery == nil || parent.Delivery.Expected != 1 {
		t.Fatalf("child completion discarded the original send receipt: %+v", parent)
	}
}

func TestTerminalCrossThreadChildDoesNotCancelReplyBeforeReceipt(t *testing.T) {
	for _, tc := range []struct{ childState, finalParentState string }{{"completed", "completed"}, {"failed", "stopped"}} {
		t.Run(tc.childState, func(t *testing.T) {
			f := setup(t)
			parent := agentTask{ID: "parent", Account: "test", Thread: "filehelper", Sender: "owner", State: "running", OwnerRequest: true, OriginTrace: "request"}
			child := agentTask{ID: "child", ParentID: parent.ID, Account: parent.Account, Thread: "other@chatroom", Sender: parent.Sender, State: tc.childState}
			for _, task := range []agentTask{parent, child} {
				if err := f.manager.agent.task(task); err != nil {
					t.Fatal(err)
				}
			}
			in := companion.Input{Account: parent.Account, Thread: parent.Thread, TaskID: parent.ID, TraceID: "reply-trace"}
			if !f.manager.taskReplyReady(in, companion.Decision{Action: "reply"}) {
				t.Fatal("prepared reply was suppressed before sending")
			}
			before, _ := f.manager.agent.lookupTask(parent.ID, parent.Account, parent.Thread)
			if before.State != "running" || !f.manager.agentTaskActive(in) {
				t.Fatalf("reply readiness settled the parent too early: %+v", before)
			}
			if err := f.manager.finishAgentTask(in, "reply"); err != nil {
				t.Fatal(err)
			}
			waiting, _ := f.manager.agent.lookupTask(parent.ID, parent.Account, parent.Thread)
			if waiting.State != "waiting_delivery" || waiting.Delivery == nil || waiting.Delivery.Trace != in.TraceID || !f.manager.agentTaskActive(in) {
				t.Fatalf("zero-count reply placeholder was lost: %+v", waiting)
			}
			if err := f.manager.updateAgentDelivery(in, bot.TaskDelivery{Trace: in.TraceID, State: "waiting_delivery", TextExpected: 1}); err != nil {
				t.Fatal(err)
			}
			if err := f.manager.updateAgentDelivery(in, bot.TaskDelivery{Trace: in.TraceID, State: "accepted", TextExpected: 1, TextAccepted: 1}); err != nil {
				t.Fatal(err)
			}
			settled, _ := f.manager.agent.lookupTask(parent.ID, parent.Account, parent.Thread)
			if settled.State != tc.finalParentState || settled.Delivery == nil || settled.Delivery.TextAccepted != 1 {
				t.Fatalf("parent was not settled from the real reply receipt: %+v", settled)
			}
		})
	}
}

func TestFailedParentReplyStillTracksActiveChild(t *testing.T) {
	f, _ := trustedAgent(t)
	cfg := f.manager.Config()
	cfg.Companion.Enabled = true
	f.manager.mu.Lock()
	f.manager.config = cfg.Config
	f.manager.mu.Unlock()
	now := time.Now()
	parent := agentTask{ID: "failed-reply-parent", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", State: "running", OwnerRequest: true}
	child := agentTask{ID: "active-child", ParentID: parent.ID, Account: parent.Account, Thread: "other@chatroom", Sender: parent.Sender, State: "waiting", Next: now.Add(time.Hour)}
	for _, task := range []agentTask{parent, child} {
		if err := f.manager.agent.task(task); err != nil {
			t.Fatal(err)
		}
	}
	in := companion.Input{Account: parent.Account, Thread: parent.Thread, TaskID: parent.ID, TraceID: "failed-reply"}
	if !f.manager.taskReplyReady(in, companion.Decision{Action: "reply"}) {
		t.Fatal("parent reply was blocked")
	}
	if err := f.manager.updateAgentDelivery(in, bot.TaskDelivery{Trace: in.TraceID, State: "waiting_delivery", TextExpected: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.finishAgentTask(in, "reply"); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.updateAgentDelivery(in, bot.TaskDelivery{Trace: in.TraceID, State: "failed", TextExpected: 1}); err != nil {
		t.Fatal(err)
	}
	waiting, _ := f.manager.agent.lookupTask(parent.ID, parent.Account, parent.Thread)
	if waiting.State != "failed" || !waiting.CrossThreadChildWait {
		t.Fatalf("failed acknowledgement lost child tracking: %+v", waiting)
	}
	child.State = "completed"
	if err := f.manager.agent.task(child); err != nil {
		t.Fatal(err)
	}
	if claimed, err := f.manager.claimAgentTask(now, func(string) bool { return false }); err != nil || claimed != nil {
		t.Fatalf("failed acknowledgement retried the parent model: %+v, %v", claimed, err)
	}
	settled, _ := f.manager.agent.lookupTask(parent.ID, parent.Account, parent.Thread)
	if settled.State != "failed" || settled.CrossThreadChildWait || settled.Delivery == nil || settled.Delivery.State != "failed" {
		t.Fatalf("child completion erased the failed reply evidence: %+v", settled)
	}
}

func TestAcceptedProgressReplyDoesNotHideRepeatedNoProgress(t *testing.T) {
	task := agentTask{State: "waiting", Goal: "未完成的工作", Checkpoint: "仍未取得结果", Runs: 1}
	for attempt := 1; attempt <= repeatedNoProgressLimit; attempt++ {
		task.Delivery = &bot.TaskDelivery{Trace: "accepted-progress", State: "accepted", TextExpected: 1, TextAccepted: 1}
		task.Actions = append(task.Actions, taskAction{Tool: "wechat_send_text", State: "completed", Result: "progress receipt"})
		stopped := task.stopRepeatedNoProgress("saved_checkpoint")
		if stopped != (attempt == repeatedNoProgressLimit) || task.NoProgressRuns != attempt {
			t.Fatalf("accepted progress reset an unchanged business task: %+v", task)
		}
		task.Runs++
	}
}
