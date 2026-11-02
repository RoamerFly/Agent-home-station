package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type manualShareNative struct{ *fakeTools }

type manualShareInputModel struct {
	calls          int
	hasRuntimeGoal bool
}

func (m *manualShareInputModel) Chat(_ context.Context, _ string, turns []map[string]string) (string, error) {
	m.calls++
	for _, turn := range turns {
		var p map[string]any
		if json.Unmarshal([]byte(turn["content"]), &p) == nil && p["kind"] == "current_message" {
			m.hasRuntimeGoal = p["runtime_context"] != nil
		}
	}
	return `{"action":"silent","messages":[],"reason":"synthetic_read_only_acceptance"}`, nil
}

func TestManualShareInputPreservesNativeSourceInsteadOfAdminWrapper(t *testing.T) {
	f := setup(t)
	c := companion.Defaults()
	c.Enabled = true
	c.Permissions.Remember = true
	empty := ""
	msg := linuxnative.Message{ID: "synthetic-share", AccountID: "test", ThreadID: "filehelper", SenderID: "synthetic-sender", ServerID: "123", Type: 49, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	original := companion.Input{Channel: "wechat", Account: msg.AccountID, Thread: msg.ThreadID, Sender: msg.SenderID, TraceID: msg.ID, OriginalText: &empty, MessageTime: msg.CreatedAt, Text: "收到分享", Source: &companion.SourceRef{InstanceID: f.manager.memory.SourceInstanceID(), MessageID: msg.ID, ServerID: msg.ServerID, Account: msg.AccountID, Thread: msg.ThreadID, Sender: msg.SenderID, MessageType: 49}}
	if err := f.manager.memory.Observe(original, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	in := original
	in.Text = "管理员要求重新总结，不能登记为用户原话"
	in.Source = nil
	in.OriginalText = nil
	if err := bindManualShareInput(f.manager, &in, msg, "[微信视频实际内容观察] 合成资料"); err != nil {
		t.Fatal(err)
	}
	if in.Source == nil || in.Source.EventID == "" || in.OriginalText == nil || *in.OriginalText != "" || !in.HistoryObserved || !in.BackgroundResult || in.OriginTraceID != msg.ID || len(in.IncomingMedia) != 1 || in.IncomingMedia[0].Source.EventID != in.Source.EventID {
		t.Fatal("manual reread lost source/observation boundaries", in)
	}
	if _, ok := f.manager.memory.SourceForInput(in); !ok {
		t.Fatal("manual reread rejected its existing source")
	}
	in.Mode = "direct"
	in.ConversationDelivery = true
	model := &manualShareInputModel{}
	engine, err := companion.New(c, f.manager.memory, model, "synthetic policy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Respond(t.Context(), in); err != nil || model.calls == 0 || !model.hasRuntimeGoal {
		t.Fatal("manual reread failed before generation or lost its explicit goal", err, model.calls, model.hasRuntimeGoal)
	}
	scope, err := f.manager.memory.Read(companion.ScopeID(in.Channel, in.Account, in.Thread), c, time.Now())
	if err != nil || len(scope.Messages) != 1 || scope.Messages[0].FullText != "" {
		t.Fatal("admin wrapper was stored as new native speech", err, len(scope.Messages))
	}
	msg.SenderID = "another-member"
	if err := bindManualShareInput(f.manager, &in, msg, "synthetic"); err == nil {
		t.Fatal("different uploader accepted")
	}
}

func (n *manualShareNative) Message(ctx context.Context, id string) (linuxnative.Message, error) {
	kind := 51
	account := "test"
	if id != "original-card" {
		account = "different-account"
	}
	return linuxnative.Message{ID: id, AccountID: account, ThreadID: "filehelper", Direction: "incoming", Type: 49, AppMessageType: &kind, Link: &linuxnative.LinkCard{Title: "公园开放", SourceName: "WeChat Channels"}}, nil
}

func TestManualShareReplyRequiresDrainAndReusesClaimWithoutModelOrResend(t *testing.T) {
	f := setup(t)
	native := &manualShareNative{&fakeTools{fakeNative: f.native}}
	f.manager.native = native
	f.manager.config.Companion = companion.Defaults()
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Behavior.ReadVideos = true
	input := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "original-card", Text: "收到了一条分享卡片"}
	if err := f.manager.memory.Observe(input, f.manager.config.Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	id := companion.ScopeID("wechat", "test", "filehelper")
	path := "/api/companion/memory/" + id + "/reply-share"
	body := map[string]string{"message_id": "original-card", "idempotency_key": "owner-share-once"}
	if code, _, _ := f.request("POST", path, body, false, false); code != 401 {
		t.Fatal("missing authentication", code)
	}
	f.login()
	if code, _, _ := f.request("POST", path, body, true, false); code != 403 {
		t.Fatal("missing CSRF", code)
	}
	if code, _, _ := f.request("POST", path, body, true, true); code != 409 {
		t.Fatal("concurrent worker not excluded", code)
	}
	f.manager.drainTicket = "test-drain"
	raw, _ := json.Marshal([]string{"owner-share-reply-v1", id, "original-card"})
	hash := sha256.Sum256(raw)
	_, _, err := f.manager.tools.claim(toolRecord{Key: "owner-share-once", Hash: hex.EncodeToString(hash[:]), Account: "test", Thread: "filehelper", Kind: "text", Status: linuxnative.ToolStatus{State: "accepted", ThreadID: "filehelper", ID: "already-sent", ServerID: "88"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		code, b, _ := f.request("POST", path, body, true, true)
		if code != 202 || !bytes.Contains(b, []byte("already-sent")) {
			t.Fatal(code, string(b))
		}
	}
	// The live window can have pruned the original: native identity, rather
	// than an indefinitely retained conversation turn, authorizes a reread.
	if err = f.manager.memory.Clear(id); err != nil {
		t.Fatal(err)
	}
	if err = f.manager.memory.Observe(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "new-turn", Text: "synthetic new request"}, f.manager.config.Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.request("POST", path, body, true, true); code != 202 {
		t.Fatal("valid pruned native share rejected", code)
	}
	if native.submits.Load() != 0 {
		t.Fatal("claimed reply was resent")
	}
	body["message_id"] = "other-card"
	if code, _, _ := f.request("POST", path, body, true, true); code != 400 {
		t.Fatal("unrelated message accepted", code)
	}
	f.manager.config.Companion.Permissions.ReplyText = false
	if code, _, _ := f.request("POST", path, body, true, true); code != 403 {
		t.Fatal("revoked permission ignored", code)
	}
}
