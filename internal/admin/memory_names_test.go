package admin

import (
	"context"
	"encoding/json"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
)

type namedNative struct{ *fakeNative }

func (*namedNative) ResolveSpeaker(context.Context, string, string) (identity.Name, error) {
	return identity.Name{Text: "联系人备注", Source: "remark"}, nil
}

func TestLogNamesAreDisplayOnlyAndPreserveAuditIDs(t *testing.T) {
	f := setup(t)
	f.login()
	f.manager.native = &namedNative{f.native}
	f.manager.record(bot.Record{Event: "message_received", Category: "received", ThreadID: "friend", Sender: "peer", Content: "合成记录", MessageID: "fixture"})
	stored := f.manager.Logs()
	before := stored[len(stored)-1]
	code, b, _ := f.request("GET", "/api/logs", nil, false, false)
	var out struct {
		Items []LogEntry `json:"items"`
	}
	json.Unmarshal(b, &out)
	if code != 200 {
		t.Fatal(code)
	}
	got := out.Items[len(out.Items)-1]
	if got.Source != "会话备注" || got.SenderName != "联系人备注" || got.ThreadID != "friend" || got.Sender != "peer" {
		t.Fatal("display identity lost", got)
	}
	after := f.manager.Logs()
	if after[len(after)-1].Source != before.Source || after[len(after)-1].SenderName != "" {
		t.Fatal("display lookup rewrote audit evidence")
	}
}
func (*namedNative) ResolveConversation(context.Context, string) (identity.Name, error) {
	return identity.Name{Text: "会话备注", Source: "remark"}, nil
}

func TestMemoryAPIBackfillsOldNamesAndDoesNotExposeAccountIdentity(t *testing.T) {
	f := setup(t)
	f.login()
	f.manager.native = &namedNative{f.native}
	c := f.manager.Config().Companion
	c.Permissions.Remember = true
	f.manager.config.Companion = c
	input := companion.Input{Channel: "wechat", Account: "test", Thread: "friend", Sender: "peer", Text: "保留原正文", TraceID: "original"}
	if err := f.manager.memory.Observe(input, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	id := companion.ScopeID(input.Channel, input.Account, input.Thread)
	code, body, _ := f.request("GET", "/api/companion/memory/"+id, nil, false, false)
	var response struct {
		Label    string           `json:"label"`
		Messages []companion.Turn `json:"messages"`
		Account  string           `json:"account"`
	}
	_ = json.Unmarshal(body, &response)
	if code != 200 || response.Label != "会话备注" || len(response.Messages) != 1 || response.Messages[0].SenderName != "联系人备注" || response.Messages[0].Sender != "peer" || response.Account != "" {
		t.Fatal(code, string(body))
	}
	stored, _ := f.manager.memory.Read(id, c, time.Now())
	if stored.Messages[0].SenderName != "联系人备注" || stored.Messages[0].TraceID != "original" || stored.Messages[0].Text != "保留原正文" {
		t.Fatal("name not recorded or content changed", stored)
	}
	other := input
	other.Account = "other"
	_ = f.manager.memory.Observe(other, c, time.Now())
	if code, _, _ := f.request("GET", "/api/companion/memory/"+companion.ScopeID(other.Channel, other.Account, other.Thread), nil, false, false); code != 404 {
		t.Fatal("foreign memory accepted", code)
	}
}
