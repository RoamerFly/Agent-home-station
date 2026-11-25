package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

func TestReminderListKeepsHistoryOutOfDefaultContextAndCanDeleteIt(t *testing.T) {
	f, p := trustedAgent(t)
	id := agentID("compact-reminder-list")
	task := agentTask{ID: id, Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, Title: "旧提醒", RequestedText: "喝水", Checkpoint: strings.Repeat("很长的历史记录", 100), State: "completed", Reminder: &reminderSpec{Kind: "notify"}}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	active, err := p.Call(t.Context(), "agent_reminder", json.RawMessage(`{"action":"list"}`))
	if err != nil || strings.Contains(active, id) {
		t.Fatal("history entered default reminder context", active, err)
	}
	history, err := p.Call(t.Context(), "agent_reminder", json.RawMessage(`{"action":"list","status":"history"}`))
	if err != nil || !strings.Contains(history, id) || strings.Contains(history, task.Checkpoint) {
		t.Fatal("history list was not compact", history, err)
	}
	deleted, err := p.Call(t.Context(), "agent_reminder", json.RawMessage(`{"action":"delete","id":"`+id+`"}`))
	if err != nil || !strings.Contains(deleted, id) {
		t.Fatal("owner could not delete ended reminder", deleted, err)
	}
}

func TestDeleteHistoricalReminderRemovesEndedHandoffButKeepsActiveWork(t *testing.T) {
	f := setup(t)
	parentID, childID := agentID("ended-reminder"), agentID("ended-handoff")
	parent := agentTask{ID: parentID, Account: "test", Thread: "filehelper", Sender: "owner", State: "completed", Reminder: &reminderSpec{Kind: "execute", FollowUpTaskID: childID}}
	child := agentTask{ID: childID, ParentID: parentID, Account: "test", Thread: "filehelper", Sender: "owner", State: "waiting", ReminderDue: true}
	for _, task := range []agentTask{parent, child} {
		if err := f.manager.agent.task(task); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.manager.agent.deleteHistoricalReminder(parentID, "test"); err == nil {
		t.Fatal("active handoff was deleted")
	}
	if _, ok := f.manager.agent.lookupTask(parentID, "test", ""); !ok {
		t.Fatal("rejected deletion lost the parent")
	}
	child.State = "completed"
	if err := f.manager.agent.task(child); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.agent.retireSettled(); err != nil {
		t.Fatal(err)
	}
	ids, err := f.manager.agent.deleteHistoricalReminder(parentID, "test")
	if err != nil || len(ids) != 2 {
		t.Fatal(ids, err)
	}
	if _, ok := f.manager.agent.lookupTask(parentID, "test", ""); ok {
		t.Fatal("deleted reminder still listed")
	}
	for _, path := range []string{filepath.Join(f.manager.agent.dir, "reminders", parentID+".json"), filepath.Join(f.manager.agent.dir, "archive", "agent-tasks", agentID(parentID)+".json"), filepath.Join(f.manager.agent.dir, "archive", "agent-tasks", agentID(childID)+".json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("historical file remains", path, err)
		}
	}
	reloaded, err := newAgentControl(f.dir)
	if err != nil || len(reloaded.taskHistory("test", "")) != 0 {
		t.Fatal("deleted reminder returned after restart", err)
	}
}

func TestDeleteCancelledReminderWithUnconfirmedDelivery(t *testing.T) {
	f := setup(t)
	id := agentID("cancelled-reminder-with-old-receipt")
	task := agentTask{ID: id, Account: "test", Thread: "filehelper", Sender: "owner", State: "cancelled", Reminder: &reminderSpec{Kind: "notify"}, PendingOperations: []string{"old-send"}, Delivery: &bot.TaskDelivery{State: "unknown", Expected: 1}}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.agent.retireSettled(); err != nil {
		t.Fatal(err)
	}
	ids, err := f.manager.agent.deleteHistoricalReminder(id, "test")
	if err != nil || len(ids) != 1 || ids[0] != id {
		t.Fatal("cancelled reminder could not be deleted", ids, err)
	}
	if _, found := f.manager.agent.lookupTask(id, "test", ""); found {
		t.Fatal("deleted cancelled reminder returned")
	}
}

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

func TestAdministratorReopensOriginalArchivedTaskForMissingWork(t *testing.T) {
	f := setup(t)
	f.login()
	id := agentID("image-and-video-original-request")
	before := agentTask{
		ID: id, Account: f.native.Account(), Thread: "test@chatroom", Sender: "owner",
		RequestThread: "test@chatroom", SourceThread: "test@chatroom", DeliveryThread: "test@chatroom",
		SourceMessageIDs: []string{"source-160"}, OriginTrace: "source-160", RequestedText: "画图并生成视频",
		Title: "画图并生成视频", Goal: "完成图片与视频并发送", Checkpoint: "原始失败检查点",
		State: "completed", Delivery: &bot.TaskDelivery{Trace: "first-image", State: "accepted", Expected: 1, Accepted: 1},
	}
	if err := f.manager.agent.task(before); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.agent.retireSettled(); err != nil {
		t.Fatal(err)
	}
	status, body, _ := f.request("POST", "/api/agent/tasks/"+id, map[string]string{
		"action": "reopen", "continuation_note": "已有图片 job_id=sample-job-1；先查询原作业，缺视频才继续，已发图片不重发",
	}, true, true)
	if status != 200 {
		t.Fatalf("reopen %d %s", status, body)
	}
	audited := false
	for _, entry := range f.manager.journal {
		if entry.Event == "agent_task_reopened" && entry.RelatedMessageID == id {
			audited = true
		}
	}
	if !audited {
		t.Fatal("reopen audit missing")
	}
	after, ok := f.manager.agent.lookupTask(id, before.Account, "")
	if !ok || after.ID != id || after.State != "pending" || after.WorkCompleted || after.Next.IsZero() ||
		after.OriginTrace != before.OriginTrace || after.Goal != before.Goal || after.RequestedText != before.RequestedText ||
		after.RequestThread != before.RequestThread || after.SourceThread != before.SourceThread || after.DeliveryThread != before.DeliveryThread ||
		len(after.SourceMessageIDs) != 1 || after.SourceMessageIDs[0] != "source-160" ||
		after.Delivery == nil || *after.Delivery != *before.Delivery ||
		!strings.Contains(after.Checkpoint, before.Checkpoint) || !strings.Contains(after.Checkpoint, "sample-job-1") ||
		!strings.Contains(after.Checkpoint, "first-image") || len(f.manager.agent.taskHistory(before.Account, "")) != 0 {
		t.Fatalf("original task was not preserved: %+v", after)
	}
	status, _, _ = f.request("POST", "/api/agent/tasks/"+id, map[string]string{"action": "reopen"}, true, true)
	if status != 409 {
		t.Fatal("duplicate reopen accepted")
	}
	// Simulate interruption after active-state persistence but before archive
	// cleanup: the active version must win on restart.
	stale, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(f.manager.agent.dir, "archive", "agent-tasks", agentID(id)+".json")
	if err := os.WriteFile(archivePath, stale, 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newAgentControl(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	restarted, ok := reloaded.lookupTask(id, before.Account, "")
	if !ok || restarted.State != "pending" || !strings.Contains(restarted.Checkpoint, "sample-job-1") || len(reloaded.taskHistory(before.Account, "")) != 0 {
		t.Fatalf("restart lost reopened task: %+v", restarted)
	}
}

func TestAdministratorCannotReopenArchivedTaskWithActiveTool(t *testing.T) {
	f := setup(t)
	f.login()
	id := agentID("archived-with-active-tool")
	task := agentTask{ID: id, Account: f.native.Account(), Thread: "filehelper", Sender: "owner", State: "completed", MusicJob: "still-running"}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.agent.retireSettled(); err != nil {
		t.Fatal(err)
	}
	status, _, _ := f.request("POST", "/api/agent/tasks/"+id, map[string]string{"action": "reopen", "continuation_note": "继续"}, true, true)
	if status != 409 || len(f.manager.agent.taskHistory(task.Account, "")) != 1 {
		t.Fatal("active tool task was reopened")
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
