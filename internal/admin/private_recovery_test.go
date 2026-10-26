package admin

import (
	"context"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/linuxnative"
)

type prefixRecoveryNative struct {
	*fakeNative
	msg linuxnative.Message
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
