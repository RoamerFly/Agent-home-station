package admin

import (
	"context"
	"encoding/json"
	"testing"
	"time"
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

func TestDeviceRecordingParametersAndSourceMemory(t *testing.T) {
	o := mobile.Observation{AllowedPackages: []string{"test.browser"}, Browsers: []string{"test.browser"}}
	if validDeviceStep(mobile.Step{Action: "capture"}, o) {
		t.Fatal("record duration omitted")
	}
	if !validDeviceStep(mobile.Step{Action: "capture", Arguments: map[string]any{"seconds": float64(4)}}, o) {
		t.Fatal("explicit recording duration rejected")
	}
	if validDeviceStep(mobile.Step{Action: "open_url"}, o) {
		t.Fatal("missing browser/url accepted")
	}
	if validDeviceStep(mobile.Step{Action: "open_url", Arguments: map[string]any{"package": "other.browser", "url": "https://example.com"}}, o) {
		t.Fatal("unlisted browser accepted")
	}
	if !validDeviceStep(mobile.Step{Action: "open_url", Arguments: map[string]any{"package": "test.browser", "url": "https://example.com"}}, o) {
		t.Fatal("valid browser rejected")
	}
	f := setup(t)
	cfg := f.manager.Config().Companion
	cfg.Permissions.Remember = true
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.mu.Unlock()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "synthetic-room", Sender: "owner", TraceID: "source", Text: "观察页面"}
	if err := f.manager.memory.Observe(in, cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.manager.rememberDevice(mobile.Task{ID: "device-synthetic", DeviceID: "lab", State: "completed", Source: mobile.Source{Account: "test", Thread: "synthetic-room", Sender: "owner", MessageID: "source"}, Result: "真实屏幕观察", Artifacts: []mobile.Artifact{{ID: "clip-synthetic", AssetID: "asset-synthetic", Audio: false, Original: false, Summary: "无音轨录屏"}}})
	scope := f.manager.memory.Context(in, cfg, time.Now())
	if len(scope.Messages) != 2 {
		t.Fatal(scope.Messages)
	}
	got := scope.Messages[1].DeviceObservation
	if got == nil || len(got.Recordings) != 1 || got.Recordings[0].AssetID != "asset-synthetic" || got.Recordings[0].Original || got.Recordings[0].Audio {
		t.Fatal(got)
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
