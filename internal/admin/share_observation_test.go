package admin

import (
	"context"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type observeShareNative struct {
	*fakeNative
	thread string
}

func (n *observeShareNative) Message(_ context.Context, id string) (linuxnative.Message, error) {
	type4 := 4
	return linuxnative.Message{ID: id, AccountID: "test", Type: 49, AppMessageType: &type4, ThreadID: n.thread, Direction: "incoming", Link: &linuxnative.LinkCard{Title: "synthetic video", URL: "http://127.0.0.1/private"}}, nil
}
func TestShareObservationUsesPrivateConfigAndCannotCrossScopeOrSend(t *testing.T) {
	f := setup(t)
	n := &observeShareNative{fakeNative: f.native, thread: "filehelper"}
	f.manager.native = n
	f.manager.config.APIKey = "synthetic-private-provider-value"
	f.manager.config.Companion.Permissions.Remember = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "original", Text: "收到了一条分享卡片"}
	if err := f.manager.memory.Observe(in, f.manager.config.Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	path := "/api/companion/memory/" + companion.ScopeID("wechat", "test", "filehelper") + "/observe-share"
	body := map[string]string{"message_id": "original"}
	if code, _, _ := f.request("POST", path, body, false, false); code != 401 {
		t.Fatal(code)
	}
	f.login()
	if code, _, _ := f.request("POST", path, body, true, false); code != 403 {
		t.Fatal(code)
	}
	code, b, _ := f.request("POST", path, body, true, true)
	// The private key is retained to construct the client, then the page's
	// loopback destination is rejected before a request/model call is made.
	if code != 502 || strings.Contains(string(b), f.manager.config.APIKey) {
		t.Fatal(code, string(b))
	}
	// A native original absent from the active memory window is still checked
	// and read. The blocked synthetic URL fails at observation, not a fake
	// missing-memory gate, and does not insert a historical turn.
	if code, _, _ := f.request("POST", path, map[string]string{"message_id": "pruned-original"}, true, true); code != 502 {
		t.Fatal("native reread blocked by live-memory retention", code)
	}
	if got := f.manager.memory.Context(in, f.manager.config.Companion, time.Now()); len(got.Messages) != 1 {
		t.Fatal("reread inserted an old message", len(got.Messages))
	}
	n.thread = "different@chatroom"
	if code, _, _ := f.request("POST", path, body, true, true); code != 400 {
		t.Fatal("cross-scope observation allowed", code)
	}
	f.manager.config.Behavior.ReadVideos = false
	if code, _, _ := f.request("POST", path, body, true, true); code != 403 {
		t.Fatal("read permission bypassed", code)
	}
}
func TestMetadataRefreshCannotEraseActualObservation(t *testing.T) {
	f := setup(t)
	f.manager.native = &receivedShareNative{f.native}
	f.manager.config.Companion.Permissions.Remember = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "original", Text: "收到了一条分享卡片\n[微信视频实际内容观察]\n[实际音轨转写]真实声音"}
	if err := f.manager.memory.Observe(in, f.manager.config.Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.login()
	path := "/api/companion/memory/" + companion.ScopeID("wechat", "test", "filehelper") + "/refresh-shares"
	code, b, _ := f.request("POST", path, map[string]any{}, true, true)
	if code != 200 || !strings.Contains(string(b), `"refreshed":0`) {
		t.Fatal(code, string(b))
	}
	scope := f.manager.memory.Context(in, f.manager.config.Companion, time.Now())
	if len(scope.Messages) != 1 || scope.Messages[0].Text != in.Text {
		t.Fatal("actual content replaced with title")
	}
}
