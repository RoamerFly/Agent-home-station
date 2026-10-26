package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/linuxnative"
)

func TestRecoveryMigrationUsesDurableIdentityInsteadOfModelAbbreviation(t *testing.T) {
	f, _ := trustedAgent(t)
	delivery := bot.TaskDelivery{Trace: "original-trace", PendingJob: "job_complete-synthetic-id"}
	source := agentTask{ID: "original-task", Account: "test", Thread: "room@chatroom", Delivery: &delivery}
	recovery := agentTask{ID: agentID("test", source.Thread, delivery.Trace, delivery.PendingJob, "delivery-recovery"), Account: "test", Thread: "wxid_owner", Title: "处理发送异常", ReportSourceThread: source.Thread, Checkpoint: "只记得 job_complete", State: "waiting", OwnerRequested: true, Sender: "wxid_owner"}
	if e := f.manager.agent.task(source); e != nil {
		t.Fatal(e)
	}
	if e := f.manager.agent.task(recovery); e != nil {
		t.Fatal(e)
	}
	a, e := newAgentControl(f.manager.agent.dir)
	if e != nil {
		t.Fatal(e)
	}
	tasks := a.list("test", "wxid_owner")
	if len(tasks) != 1 || tasks[0].DeliveryJob != delivery.PendingJob {
		t.Fatal("guessed identity from prose", tasks)
	}
}
func TestRecoveryCannotCompleteWhileOriginalDeliveryUnknown(t *testing.T) {
	f, p := trustedAgent(t)
	n := &deliveryToolNative{fakeTools: &fakeTools{fakeNative: f.native}, status: linuxnative.ToolStatus{ID: "job", ThreadID: "room@chatroom", State: "needs_review"}}
	f.manager.native = n
	task := agentTask{ID: "recovery", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", OwnerRequested: true, ReportSourceThread: "room@chatroom", DeliveryJob: "job", State: "running"}
	if e := f.manager.agent.task(task); e != nil {
		t.Fatal(e)
	}
	args := json.RawMessage(`{"id":"recovery","checkpoint":"已经查询","state":"completed"}`)
	result, e := p.Call(context.Background(), "agent_task", args)
	if e != nil || !strings.Contains(result, `"state":"waiting"`) {
		t.Fatal(result, e)
	}
	n.status.State = "accepted"
	n.status.ServerID = "123"
	result, e = p.Call(context.Background(), "agent_task", args)
	if e != nil || !strings.Contains(result, `"state":"completed"`) {
		t.Fatal(result, e)
	}
}
