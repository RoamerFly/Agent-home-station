package admin

import (
	"context"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type prefixRecoveryNative struct {
	*fakeNative
	msg linuxnative.Message
}

func TestRecoveredPrivateReplyRecordsProcessingAndActualMediaDelivery(t *testing.T) {
	f, _ := trustedAgent(t)
	task := agentTask{ID: "recovered", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", RecoveryMessageID: "original", State: "running"}
	if e := f.manager.agent.task(task); e != nil {
		t.Fatal(e)
	}
	in := companion.Input{Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TaskID: task.ID, TraceID: "recover-recovered"}
	f.manager.finishAgentTask(in, "reply")
	if got := f.manager.agent.list("test", in.Thread)[0]; got.State != "completed" || got.Delivery != nil {
		t.Fatal("processed reply invented a receipt or requested review", got)
	}
	delivery := bot.TaskDelivery{Trace: in.TraceID, Expected: 1, Accepted: 0, State: "partial"}
	if e := f.manager.updateAgentDelivery(in, delivery); e != nil {
		t.Fatal(e)
	}
	if got := f.manager.agent.list("test", in.Thread)[0]; got.State != "waiting_delivery" {
		t.Fatal("media state was not tracked", got)
	}
	delivery.State, delivery.Accepted = "accepted", 1
	if e := f.manager.updateAgentDelivery(in, delivery); e != nil {
		t.Fatal(e)
	}
	if got := f.manager.agent.list("test", in.Thread)[0]; got.State != "completed" || got.Delivery.Accepted != 1 {
		t.Fatal("real media receipt not retained", got)
	}
}

func (n *prefixRecoveryNative) Message(context.Context, string) (linuxnative.Message, error) {
	return n.msg, nil
}
func TestPrivateRecoveryKeepsRealSenderAndCannotReplayProcessedText(t *testing.T) {
	f, _ := trustedAgent(t)
	f.login()
	n := &prefixRecoveryNative{fakeNative: f.native, msg: linuxnative.Message{ID: "original", ThreadID: "wxid_owner", SenderID: "wxid_owner", ServerID: "123", Type: 1, Direction: "incoming", Body: "检查代码", CreatedAt: time.Now().UTC().Format(time.RFC3339)}}
	f.manager.native = n
	data := map[string]string{"message_id": "original"}
	if code, _, _ := f.request("POST", "/api/agent/recover-private-message", data, true, true); code != 409 {
		t.Fatal(code)
	}
	f.manager.record(bot.Record{Event: "message_skipped", ThreadID: "wxid_owner", MessageID: "original", Reason: "private_prefix_not_matched_or_empty"})
	if code, _, _ := f.request("POST", "/api/agent/recover-private-message", data, true, true); code != 201 {
		t.Fatal(code)
	}
	tasks := f.manager.agent.list("test", "wxid_owner")
	if len(tasks) != 1 || tasks[0].Sender != "wxid_owner" || tasks[0].Checkpoint != "检查代码" || !tasks[0].OwnerRequested {
		t.Fatal(tasks)
	}
	if code, _, _ := f.request("POST", "/api/agent/recover-private-message", data, true, true); code != 200 {
		t.Fatal(code)
	}
	n.msg.ID = "already"
	f.manager.record(bot.Record{Event: "message_skipped", ThreadID: "wxid_owner", MessageID: "already", Reason: "private_prefix_not_matched_or_empty"})
	f.manager.record(bot.Record{Event: "message_processing", ThreadID: "wxid_owner", MessageID: "already"})
	if code, _, _ := f.request("POST", "/api/agent/recover-private-message", map[string]string{"message_id": "already"}, true, true); code != 409 {
		t.Fatal(code)
	}
}
