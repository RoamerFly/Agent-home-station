package bot

import (
	"context"
	"testing"
	"wechat-bot/internal/linuxnative"
)

func TestActivityLogsUnapprovedReceivedTextWithoutCallingModel(t *testing.T) {
	message := fixtureMessage("unapproved", "other@chatroom", "@小号 正文", "incoming")
	source := &fakeSource{messages: map[string]linuxnative.Message{message.ID: message}}
	model := &fakeModel{answer: "answer"}
	sender := &fakeSender{}
	worker := newTestWorker(t, source, model, sender)
	records := []Record{}
	worker.config.Record = func(record Record) { records = append(records, record) }
	worker.handle(context.Background(), fixtureEvent("1", message.ID, message.ThreadID, "incoming", "sync"))
	if model.calls != 0 || sender.calls != 0 || len(records) != 2 || records[0].Content != message.Body || records[1].Reason != "conversation_not_allowed" {
		t.Fatal("logging expanded model permissions", records)
	}
}
func TestActivityKeepsSentContentAndReceiptStatus(t *testing.T) {
	message := fixtureMessage("one", "filehelper", "/ai 问题", "outgoing")
	source := &fakeSource{messages: map[string]linuxnative.Message{message.ID: message}}
	model := &fakeModel{answer: "回复正文"}
	sender := &fakeSender{}
	worker := newTestWorker(t, source, model, sender)
	records := []Record{}
	worker.config.Record = func(r Record) { records = append(records, r) }
	worker.handle(context.Background(), fixtureEvent("1", message.ID, message.ThreadID, "outgoing", "sync"))
	last := records[len(records)-1]
	if last.Category != "sent" || last.Status != "accepted" || last.Content != "回复正文" {
		t.Fatal("missing sent text/receipt", last)
	}
}
