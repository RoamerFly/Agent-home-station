package bot

import (
	"context"
	"strings"
	"testing"
	"wechat-bot/internal/linuxnative"
)

func TestLegacyLongReplyIsNeverTruncatedOrPartitioned(t *testing.T) {
	message := fixtureMessage("long-output", "friend", "请完整回复", "incoming")
	source := &fakeSource{messages: map[string]linuxnative.Message{message.ID: message}}
	model := &fakeModel{answer: strings.Repeat("完整正文", 1100) + "不可丢失的末尾"}
	sender := &fakeSender{}
	w := newTestWorker(t, source, model, sender)
	var failure bool
	w.config.Record = func(r Record) {
		failure = failure || r.Event == "model_failed" && r.Reason == "model_reply_text_too_long" && r.Status == "failed"
	}
	w.handle(context.Background(), fixtureEvent("1", message.ID, message.ThreadID, "incoming", "remote_sync"))
	if model.calls != 1 || sender.calls != 0 || !failure {
		t.Fatalf("long text truncated/partitioned or diagnosis missing: calls=%d sends=%d failure=%v", model.calls, sender.calls, failure)
	}
}
