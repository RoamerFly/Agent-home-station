package admin

import (
	"encoding/json"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestPreferencesEndpointScopedVersionedAndAuthenticated(t *testing.T) {
	f := setup(t)
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "seed", Text: "合成资料"}
	if err := f.manager.memory.Observe(in, f.manager.config.Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	id := companion.ScopeID(in.Channel, in.Account, in.Thread)
	path := "/api/companion/memory/" + id + "/preferences"
	body := map[string]any{"text": "收到分享后默认总结主要内容", "revision": 0}
	if code, _, _ := f.request("POST", path, body, false, false); code != 401 {
		t.Fatal(code)
	}
	f.login()
	if code, _, _ := f.request("POST", path, body, true, false); code != 403 {
		t.Fatal(code)
	}
	if code, _, _ := f.request("POST", path, body, true, true); code != 200 {
		t.Fatal(code)
	}
	if code, _, _ := f.request("POST", path, body, true, true); code != 409 {
		t.Fatal("stale preferences accepted", code)
	}
	code, b, _ := f.request("GET", "/api/companion/memory/"+id, nil, true, false)
	var out struct {
		Preferences string `json:"preferences"`
		Revision    int    `json:"preferences_revision"`
	}
	if code != 200 || json.Unmarshal(b, &out) != nil || out.Preferences != body["text"] || out.Revision != 1 {
		t.Fatal(code, string(b))
	}
}
