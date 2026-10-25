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

func (n *manualShareNative) Message(ctx context.Context, id string) (linuxnative.Message, error) {
	kind := 51
	return linuxnative.Message{ID: id, ThreadID: "filehelper", Direction: "incoming", Type: 49, AppMessageType: &kind, Link: &linuxnative.LinkCard{Title: "公园开放", SourceName: "WeChat Channels"}}, nil
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
