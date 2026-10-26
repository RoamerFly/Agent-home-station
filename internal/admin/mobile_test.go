package admin

import (
	"context"
	"encoding/json"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mobile"
)

func TestMobileRoutesRequireAuthenticationAndCSRF(t *testing.T) {
	f := setup(t)
	if status, _, _ := f.request("GET", "/api/mobile", nil, false, false); status != 401 {
		t.Fatal(status)
	}
	f.login()
	status, data, _ := f.request("GET", "/api/mobile", nil, false, false)
	if status != 200 {
		t.Fatal(status, string(data))
	}
	var view struct {
		Settings mobile.Settings `json:"settings"`
	}
	if json.Unmarshal(data, &view) != nil {
		t.Fatal("decode")
	}
	if status, _, _ := f.request("POST", "/api/mobile/config", view.Settings, true, false); status != 403 {
		t.Fatal(status)
	}
	if status, _, _ := f.request("POST", "/api/mobile/config", view.Settings, true, true); status != 200 {
		t.Fatal(status)
	}
	if status, _, _ := f.request("POST", "/api/mobile/config", view.Settings, true, true); status != 409 {
		t.Fatal("stale write", status)
	}
	if status, _, _ := f.request("POST", "/api/mobile/tasks", map[string]string{"request_key": "safe", "device_id": "lab", "goal": "view"}, true, true); status != 409 {
		t.Fatal("disabled start", status)
	}
	if status, _, _ := f.request("GET", "/assets/mobile.js", nil, false, false); status != 200 {
		t.Fatal("asset", status)
	}
}
func TestMobileToolsReuseTrustedIdentityAndNeverPreviewOrSurf(t *testing.T) {
	f, p := trustedAgent(t)
	s := f.manager.mobile.Settings()
	s.Enabled = true
	s.Devices = []string{"lab"}
	s.Bridge.Command = "synthetic-not-executed"
	if e := f.manager.mobile.Save(s); e != nil {
		t.Fatal(e)
	}
	if !p.mobileToolEnabled("device_tasks") {
		t.Fatal("trusted tool missing")
	}
	for _, in := range []companion.Input{{Account: "test", Thread: "wxid_owner", Sender: "wxid_other"}, {Account: "test", Thread: "other", Sender: "wxid_owner"}, {Account: "other", Thread: "wxid_owner", Sender: "wxid_owner"}, {Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", Surf: true}, {Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", Preview: true}} {
		p.SetTrace(in)
		if p.mobileToolEnabled("device_task_start") {
			t.Fatal("untrusted discovery")
		}
		if _, e := p.Call(context.Background(), "device_task_start", json.RawMessage(`{"device_id":"lab","goal":"view"}`)); e == nil {
			t.Fatal("untrusted execution")
		}
	}
}
