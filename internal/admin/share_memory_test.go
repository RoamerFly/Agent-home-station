package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type receivedShareNative struct{ *fakeNative }

type receivedQuotedNative struct{ *fakeNative }

func (n *receivedQuotedNative) Message(_ context.Context, id string) (linuxnative.Message, error) {
	type57 := 57
	return linuxnative.Message{ID: id, AccountID: "test", ThreadID: "filehelper", Type: 49, AppMessageType: &type57, Body: "还是文件吧", Quote: &linuxnative.QuoteReference{ServerID: "888", Type: 1}}, nil
}
func (n *receivedQuotedNative) QuotedMessage(_ context.Context, id string) (linuxnative.QuoteResolution, error) {
	return linuxnative.QuoteResolution{AccountID: "test", MessageID: id, ThreadID: "filehelper", State: "resolved", Message: &linuxnative.Message{AccountID: "test", ThreadID: "filehelper", ServerID: "888", Type: 1, Body: "这首可以给你原音频", Direction: "outgoing"}}, nil
}

func TestRefreshIncorrectSharePlaceholderRestoresQuotedReplyWithoutSending(t *testing.T) {
	f := setup(t)
	f.manager.native = &receivedQuotedNative{f.native}
	f.manager.config.Companion.Permissions.Remember = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "quote-message", Sender: "same-person", Text: "收到了一条分享卡片，请结合介绍决定是否回应。没有可读取的标题或简介"}
	when := time.Now().Add(-time.Minute)
	if err := f.manager.memory.Observe(in, f.manager.config.Companion, when); err != nil {
		t.Fatal(err)
	}
	id := companion.ScopeID("wechat", "test", "filehelper")
	before, _ := f.manager.memory.Read(id, f.manager.config.Companion, time.Now())
	f.login()
	code, body, _ := f.request("POST", "/api/companion/memory/"+id+"/refresh-shares", map[string]any{}, true, true)
	after, err := f.manager.memory.Read(id, f.manager.config.Companion, time.Now())
	if code != 200 || err != nil || !strings.Contains(string(body), `"refreshed":1`) || !strings.Contains(after.Messages[0].Text, "还是文件吧") || !strings.Contains(after.Messages[0].Text, "这首可以给你原音频") || strings.Contains(after.Messages[0].Text, "分享卡片") || after.Messages[0].TraceID != before.Messages[0].TraceID || after.Messages[0].Sender != before.Messages[0].Sender || !after.Messages[0].Time.Equal(before.Messages[0].Time) {
		t.Fatal(code, string(body), err, after)
	}
}

func (n *receivedShareNative) Message(_ context.Context, id string) (linuxnative.Message, error) {
	kind := 51
	return linuxnative.Message{ID: id, ThreadID: "filehelper", Type: 49, AppMessageType: &kind, Link: &linuxnative.LinkCard{Title: "公园一月开放", SourceName: "WeChat Channels"}}, nil
}
func TestRefreshShareMemoryPreservesSpeakerAndTimestampWithoutSending(t *testing.T) {
	f := setup(t)
	f.manager.native = &receivedShareNative{f.native}
	f.manager.config.Companion.Permissions.Remember = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "card-message", Sender: "same-person", Text: "收到了一条分享卡片，没有可读取的标题或简介"}
	if e := f.manager.memory.Observe(in, f.manager.config.Companion, time.Now()); e != nil {
		t.Fatal(e)
	}
	id := companion.ScopeID("wechat", "test", "filehelper")
	before, e := f.manager.memory.Read(id, f.manager.config.Companion, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	f.login()
	path := "/api/companion/memory/" + id + "/refresh-shares"
	if code, _, _ := f.request("POST", path, map[string]any{}, true, false); code != 403 {
		t.Fatal("refresh omitted CSRF")
	}
	code, b, _ := f.request("POST", path, map[string]any{}, true, true)
	var result struct {
		Refreshed int `json:"refreshed"`
	}
	_ = json.Unmarshal(b, &result)
	if code != 200 || result.Refreshed != 1 {
		t.Fatal(code, string(b))
	}
	after, e := f.manager.memory.Read(id, f.manager.config.Companion, time.Now())
	if e != nil || after.Messages[0].Sender != before.Messages[0].Sender || !after.Messages[0].Time.Equal(before.Messages[0].Time) || !strings.Contains(after.Messages[0].Text, "公园一月开放") {
		t.Fatal("observation not safely refreshed", e)
	}
}
