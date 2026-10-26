package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestTaskContinuationRetainsGoalAndRealActionEvidence(t *testing.T) {
	f, p := trustedAgent(t)
	f.manager.config.Companion.Enabled = true
	task := agentTask{ID: "work", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", OwnerRequested: true, Checkpoint: "修复文件并启动后关闭本次服务", State: "running"}
	if e := f.manager.agent.task(task); e != nil {
		t.Fatal(e)
	}
	p.trace.TaskID = task.ID
	p.rememberTaskAction("server_exec", json.RawMessage(`{"path":"/synthetic","command":"stop-only-owned-probe"}`), `{"exit_code":0,"output":"process_exited=true port_closed=true"}`, nil)
	_, e := p.Call(context.Background(), "agent_task", json.RawMessage(`{"id":"work","checkpoint":"程序已关闭，整理反馈","state":"waiting","delay_seconds":30}`))
	if e != nil {
		t.Fatal(e)
	}
	tsk := f.manager.agent.list("test", "wxid_owner")[0]
	if len(tsk.Actions) != 2 || tsk.Goal != task.Checkpoint {
		t.Fatal("checkpoint lost original goal/evidence", tsk)
	}
	tsk.Next = time.Now().Add(-time.Second)
	if e = f.manager.agent.task(tsk); e != nil {
		t.Fatal(e)
	}
	in, e := f.manager.claimAgentTask(time.Now(), func(string) bool { return false })
	if e != nil || in == nil || !strings.Contains(in.Text, "process_exited=true") || !strings.Contains(in.Text, "修复文件并启动后关闭") {
		t.Fatal(in, e)
	}
	p.trace.Sender = "not-owner"
	p.rememberTaskAction("server_exec", json.RawMessage(`{}`), "untrusted", nil)
	if len(f.manager.agent.list("test", "wxid_owner")[0].Actions) != 2 {
		t.Fatal("non-owner overwrote action evidence")
	}
}
func TestOwnerModelFailureSchedulesContinuation(t *testing.T) {
	f, _ := trustedAgent(t)
	task := agentTask{ID: "work", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", OwnerRequested: true, State: "running", Checkpoint: "先核对已完成的行动"}
	if e := f.manager.agent.task(task); e != nil {
		t.Fatal(e)
	}
	f.manager.finishAgentTask(companion.Input{Account: "test", Thread: "wxid_owner", TaskID: task.ID}, "")
	if got := f.manager.agent.list("test", "wxid_owner")[0]; got.State != "waiting" || got.Next.Before(time.Now()) {
		t.Fatal("model error abandoned owner task", got)
	}
}

func TestTaskSendEvidenceAdvancesFromQueuedToVerifiedReceipt(t *testing.T) {
	f, p := trustedAgent(t)
	task := agentTask{ID: "receipt-task", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", State: "running"}
	if e := f.manager.agent.task(task); e != nil {
		t.Fatal(e)
	}
	p.trace.TaskID = task.ID
	p.rememberTaskAction("wechat_send_text", json.RawMessage(`{"text":"你好"}`), `{"operation_id":"op","state":"queued"}`, nil)
	p.rememberTaskAction("wechat_operation_status", json.RawMessage(`{"operation_id":"op"}`), `{"operation_id":"op","state":"accepted","server_id":"synthetic-receipt"}`, nil)
	got := f.manager.agent.list("test", "wxid_owner")[0]
	if len(got.Actions) != 1 || got.Actions[0].State != "accepted" || !strings.Contains(got.Actions[0].Result, "synthetic-receipt") {
		t.Fatal(got)
	}
}
