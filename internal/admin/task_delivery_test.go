package admin

import (
	"context"
	"encoding/json"
	"path/filepath"
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
	f.login()
	code, _, _ := f.request("POST", "/api/agent/tasks/"+task.ID, map[string]string{"action": "resume"}, true, true)
	if code != 409 || f.manager.agent.list("test", "filehelper")[0].State != "completed" {
		t.Fatal("resume replayed an existing delivery", code)
	}
}

func TestProgressTextReceiptDoesNotCompleteUnfinishedGroupTask(t *testing.T) {
	f := setup(t)
	task := agentTask{ID: "image-task", Account: "test", Thread: "room@chatroom", Sender: "member", OriginTrace: "image-request", State: "running"}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	in := companion.Input{Account: "test", Thread: task.Thread, TaskID: task.ID, TraceID: "progress-text"}
	f.manager.finishAgentTask(in, "reply")
	delivery := bot.TaskDelivery{Trace: in.TraceID, State: "accepted", TextExpected: 1, TextAccepted: 1}
	if err := f.manager.updateAgentDelivery(in, delivery); err != nil {
		t.Fatal(err)
	}
	got, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok || got.State != "waiting" || got.WorkCompleted || got.Next.Before(time.Now()) {
		t.Fatal("progress text prematurely completed the image request", got)
	}
}

func TestSilentDocumentRetryWithNoDeliveryCanResume(t *testing.T) {
	f := setup(t)
	task := agentTask{ID: "document-read", Account: "test", Thread: "filehelper", Sender: "owner", State: "waiting", Delivery: &bot.TaskDelivery{Trace: "empty", State: "waiting_delivery"}}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	f.login()
	code, body, _ := f.request("POST", "/api/agent/tasks/"+task.ID, map[string]string{"action": "resume"}, true, true)
	if code != 200 {
		t.Fatalf("empty plan blocked retry: %d %s", code, body)
	}
	got := f.manager.agent.list("test", "filehelper")[0]
	if got.Delivery != nil || got.State != "pending" {
		t.Fatal(got)
	}
}

func TestCompletedTaskDoesNotGetZeroCountDeliveryPlaceholder(t *testing.T) {
	f := setup(t)
	task := agentTask{ID: "completed-without-delivery", Account: "test", Thread: "filehelper", Sender: "owner", State: "running", WorkCompleted: true}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	f.manager.finishAgentTask(companion.Input{Account: task.Account, Thread: task.Thread, TaskID: task.ID, TraceID: "late-reply"}, "reply")
	got := f.manager.agent.list(task.Account, task.Thread)[0]
	if got.Delivery != nil || got.State == "waiting_delivery" {
		t.Fatalf("completed task acquired a fictitious delivery checkpoint: %+v", got)
	}
}

func TestCompletedTaskWithoutWorkCompletedDoesNotGetDeliveryPlaceholder(t *testing.T) {
	f := setup(t)
	task := agentTask{ID: "completed-without-work-flag", Account: "test", Thread: "filehelper", Sender: "owner", State: "completed"}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	f.manager.agent.mu.Lock()
	saved := f.manager.agent.data.Tasks[task.ID]
	saved.WorkCompleted = false
	f.manager.agent.data.Tasks[task.ID] = saved
	if err := f.manager.agent.persist(); err != nil {
		f.manager.agent.mu.Unlock()
		t.Fatal(err)
	}
	f.manager.agent.mu.Unlock()
	f.manager.finishAgentTask(companion.Input{Account: task.Account, Thread: task.Thread, TaskID: task.ID, TraceID: "late-completed-reply"}, "reply")
	got := f.manager.agent.list(task.Account, task.Thread)[0]
	if got.Delivery != nil || got.State == "waiting_delivery" {
		t.Fatalf("completed task without work flag acquired a fictitious delivery checkpoint: %+v", got)
	}
}

func TestDuplicateWatcherOnlyClearsZeroCountDelivery(t *testing.T) {
	empty := agentTask{Delivery: &bot.TaskDelivery{State: "waiting_delivery"}}
	clearZeroCountTaskDelivery(&empty)
	if empty.Delivery != nil {
		t.Fatalf("zero-count duplicate placeholder was retained: %+v", empty.Delivery)
	}
	withReceipt := agentTask{Delivery: &bot.TaskDelivery{State: "waiting_delivery", Expected: 1}}
	clearZeroCountTaskDelivery(&withReceipt)
	if withReceipt.Delivery == nil {
		t.Fatal("non-empty delivery evidence was cleared")
	}
}

func TestStartupSettlesCompletedProcessPlaceholderWithoutTouchingValidWaits(t *testing.T) {
	f := setup(t)
	base := func(id string) agentTask {
		return agentTask{ID: id, Account: "test", Thread: "filehelper", Sender: "owner", Title: "后台程序", ProcessID: "process-" + id, OriginTrace: "source-" + id, State: "waiting_delivery", WorkCompleted: true, Delivery: &bot.TaskDelivery{Trace: "delivery-" + id, State: "waiting_delivery"}}
	}
	completed := base("completed-process")
	validWaiting := base("valid-waiting")
	validWaiting.State = "waiting"
	secondWaiting := base("second-valid-waiting")
	secondWaiting.State = "waiting"
	secondWaiting.Delivery = nil
	withReceipt := base("with-receipt")
	withReceipt.Delivery.Expected = 1
	withPending := base("with-pending")
	withPending.PendingOperations = []string{"send-result"}
	for _, task := range []agentTask{completed, validWaiting, secondWaiting, withReceipt, withPending} {
		if err := f.manager.agent.task(task); err != nil {
			t.Fatal(err)
		}
	}
	reloaded, err := newAgentControl(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.lookupTask(completed.ID, completed.Account, completed.Thread)
	if !ok || got.State != "completed" || got.Delivery != nil || got.ProcessID != completed.ProcessID || got.OriginTrace != completed.OriginTrace {
		t.Fatalf("completed process placeholder was not safely archived: %+v", got)
	}
	checks := []struct {
		id                string
		state             string
		deliveryNil       bool
		expected          int
		pendingOperations int
	}{
		{validWaiting.ID, "waiting", false, 0, 0},
		{secondWaiting.ID, "waiting", true, 0, 0},
		{withReceipt.ID, "waiting_delivery", false, 1, 0},
		{withPending.ID, "waiting_delivery", false, 0, 1},
	}
	for _, check := range checks {
		got, ok = reloaded.lookupTask(check.id, "test", "filehelper")
		if !ok || got.State != check.state || (got.Delivery == nil) != check.deliveryNil || got.Delivery != nil && got.Delivery.Expected != check.expected || len(got.PendingOperations) != check.pendingOperations {
			t.Fatalf("valid waiting task was changed: %s %+v", check.id, got)
		}
	}
}

func TestClaimSettlesCompletedProcessPlaceholderCreatedAfterStartup(t *testing.T) {
	f := setup(t)
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.mu.Unlock()
	task := agentTask{ID: "claim-process", Account: "test", Thread: "filehelper", Sender: "owner", Title: "后台程序", ProcessID: "process-claim", OriginTrace: "source-claim", State: "waiting_delivery", WorkCompleted: true, Delivery: &bot.TaskDelivery{Trace: "delivery-claim", State: "waiting_delivery"}}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.claimAgentTask(time.Now().Add(time.Minute), func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	got, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok || got.State != "completed" || got.Delivery != nil || got.ProcessID != task.ProcessID {
		t.Fatalf("claim did not settle completed process placeholder: %+v", got)
	}
}
func TestTaskToolReturnsCommittedDeliveryState(t *testing.T) {
	f, p := trustedAgent(t)
	task := agentTask{ID: "pending-receipt", Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, State: "running", PendingOperations: []string{"awaiting-receipt"}}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	result, err := p.Call(context.Background(), "agent_task", json.RawMessage(`{"id":"pending-receipt","state":"completed","checkpoint":"work finished"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct{ Task agentTask }
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		t.Fatal(err)
	}
	saved, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok || response.Task.State != "waiting_delivery" || response.Task.State != saved.State || !response.Task.WorkCompleted {
		t.Fatalf("response=%+v stored=%+v", response.Task, saved)
	}
}

func TestProcessWatcherIdentityIncludesProcessID(t *testing.T) {
	f := setup(t)
	f.manager.config.Companion.Enabled = true
	for _, item := range []struct{ id, process string }{{"old", "a"}, {"distinct", "b"}, {"new", "a"}} {
		task := agentTask{ID: item.id, ProcessID: item.process, Title: "后台程序", OriginTrace: "shared-request", Account: "test", Thread: "filehelper", Sender: "owner", State: "waiting", Next: time.Now().Add(time.Hour)}
		if err := f.manager.agent.task(task); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.manager.claimAgentTask(time.Now(), func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	active := map[string]int{}
	for _, task := range f.manager.agent.active("test", "filehelper") {
		if task.State == "waiting" {
			active[task.ProcessID]++
		}
	}
	if active["a"] != 1 || active["b"] != 1 {
		t.Fatal(active)
	}
}

func TestFinishAgentTaskReportsCheckpointWriteFailure(t *testing.T) {
	for _, state := range []string{"yielded_to_message", "silent"} {
		t.Run(state, func(t *testing.T) {
			f := setup(t)
			task := agentTask{ID: "write-failure", ProcessID: "running-process", Account: "test", Thread: "filehelper", Sender: "owner", State: "running"}
			if err := f.manager.agent.task(task); err != nil {
				t.Fatal(err)
			}
			original := f.manager.agent.dir
			f.manager.agent.dir = filepath.Join(original, "agent-state.json")
			defer func() { f.manager.agent.dir = original }()
			f.manager.finishAgentTask(companion.Input{Account: task.Account, Thread: task.Thread, TaskID: task.ID}, state)
			saved, _ := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
			if saved.State != "running" {
				t.Fatal(saved.State)
			}
			for _, entry := range f.manager.logs {
				if entry.Event == "task_checkpoint_save_failed" {
					return
				}
			}
			t.Fatal("missing persistence failure diagnostic")
		})
	}
}
func TestMusicTaskReuseRequiresSameConversationAndRequest(t *testing.T) {
	for _, tc := range []struct {
		name, thread, origin string
		reuse                bool
	}{
		{"same request", "wxid_owner", "turn-1", true},
		{"different conversation", "room@chatroom", "turn-1", false},
		{"different request", "wxid_owner", "earlier", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, p := trustedAgent(t)
			old := agentTask{ID: "old-song-task", Title: "测试歌曲", MusicJob: "old-job", Account: p.trace.Account, Thread: tc.thread, OriginTrace: tc.origin, Sender: p.trace.Sender, State: "waiting"}
			if err := f.manager.agent.task(old); err != nil {
				t.Fatal(err)
			}
			result, err := p.Call(context.Background(), "agent_task", json.RawMessage(`{"key":"song","title":"测试歌曲","state":"waiting","checkpoint":"wait for job"}`))
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Task   agentTask
				Reused bool `json:"reused_existing"`
			}
			if err := json.Unmarshal([]byte(result), &response); err != nil {
				t.Fatal(err)
			}
			if response.Reused != tc.reuse || (response.Task.ID == old.ID) != tc.reuse {
				t.Fatal(result)
			}
		})
	}
}

func TestMusicTaskWaitsForDeliveryPermissionWithoutRepeatedWrites(t *testing.T) {
	f, p := trustedAgent(t)
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.SendVoice = false
	f.manager.config.Companion.Permissions.SendFile = false
	f.manager.mu.Unlock()
	task := agentTask{ID: "permission-wait", MusicJob: "old-job", Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, State: "waiting"}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	var saved agentTask
	for i := 0; i < 2; i++ {
		in, err := f.manager.claimAgentTask(time.Now(), func(string) bool { return false })
		if err != nil || in != nil {
			t.Fatal(in, err)
		}
		got, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
		if !ok || got.LastResult == "" || got.State != "waiting" || got.MusicJob != task.MusicJob {
			t.Fatal(got)
		}
		if i > 0 && !got.Updated.Equal(saved.Updated) {
			t.Fatal("repeated unchanged update")
		}
		saved = got
	}
}
