package bot

import (
	"context"
	"strings"
	"testing"
	"wechat-bot/internal/linuxnative"
)

func TestLegacyLongReplyIsAcceptedWithoutTruncation(t *testing.T) {
	message := fixtureMessage("long-output", "friend", "请完整回复", "incoming")
	source := &fakeSource{messages: map[string]linuxnative.Message{message.ID: message}}
	model := &fakeModel{answer: strings.Repeat("完整正文", 1100) + "不可丢失的末尾"}
	sender := &fakeSender{}
	w := newTestWorker(t, source, model, sender)
	w.handle(context.Background(), fixtureEvent("1", message.ID, message.ThreadID, "incoming", "remote_sync"))
	if model.calls != 1 || sender.calls != 1 {
		t.Fatalf("long text was truncated or rejected: calls=%d sends=%d", model.calls, sender.calls)
	}
}
