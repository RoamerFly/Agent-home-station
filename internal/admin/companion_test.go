package admin

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
)

func TestCompanionAPIsRequireAuthAndScopeIsolation(t *testing.T) {
	f := setup(t)
	if code, _, _ := f.request("GET", "/api/companion/state", nil, false, false); code != 401 {
		t.Fatal("metadata accessible without login")
	}
	f.login()
	c := f.manager.Config().Companion
	c.Permissions.Remember = true
	input := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Text: "local-memory-text", Mode: "direct"}
	if err := f.manager.memory.Observe(input, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	other := input
	other.Account = "different-account"
	if err := f.manager.memory.Observe(other, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	id := companion.ScopeID("wechat", "test", "filehelper")
	code, data, _ := f.request("GET", "/api/companion/state", nil, false, false)
	if code != 200 || bytes.Contains(data, []byte(input.Text)) || bytes.Contains(data, []byte(companion.ScopeID("wechat", other.Account, other.Thread))) {
		t.Fatal("metadata exposes conversation or another account")
	}
	path := "/api/companion/memory/" + id
	code, data, _ = f.request("GET", path, nil, false, false)
	if code != 200 || !bytes.Contains(data, []byte(input.Text)) {
		t.Fatal("explicit memory read failed")
	}
	if code, _, _ := f.request("GET", "/api/companion/memory/"+companion.ScopeID("wechat", other.Account, other.Thread), nil, false, false); code != 404 {
		t.Fatal("other account scope readable")
	}
	if code, _, _ := f.request("POST", path+"/notes", map[string]string{"text": "manual note"}, true, false); code != 403 {
		t.Fatal("notes missing CSRF check")
	}
	if code, _, _ := f.request("POST", path+"/notes", map[string]string{"text": "manual note"}, true, true); code != 200 {
		t.Fatal("notes failed")
	}
	if code, _, _ := f.request("POST", path+"/clear", map[string]any{}, true, true); code != 200 {
		t.Fatal("clear failed")
	}
	code, data, _ = f.request("GET", path, nil, false, false)
	if code != 200 || bytes.Contains(data, []byte(input.Text)) || bytes.Contains(data, []byte("manual note")) {
		t.Fatal("clear did not remove local content")
	}
}
func TestDisabledPreviewDoesNotReadMemoryOrCallProvider(t *testing.T) {
	f := setup(t)
	f.login()
	code, data, _ := f.request("POST", "/api/companion/preview", map[string]string{"mode": "direct", "text": "hello"}, true, true)
	if code != 200 || !bytes.Contains(data, []byte(`"dry_run":true`)) || !bytes.Contains(data, []byte("character_disabled")) {
		t.Fatal("disabled preview should be blocked without a provider key", code, string(data))
	}
	if code, _, _ := f.request("POST", "/api/companion/preview", map[string]string{"mode": "send", "text": "hello"}, true, true); code != 400 {
		t.Fatal("invalid preview mode accepted")
	}
	if _, err := f.manager.preview(context.Background(), "direct", strings.Repeat("x", 8193)); err == nil {
		t.Fatal("preview size unbounded")
	}
}
