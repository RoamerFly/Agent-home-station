package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mobile"
)

func TestMobileStorageFailureDoesNotPreventChatManagerStartup(t *testing.T) {
	dir := t.TempDir()
	legacyDir := filepath.Join(dir, "mobile")
	broken := []byte(`{"enabled":`)
	writeLegacyMobileState(t, legacyDir, broken, []byte(`{}`))
	m, err := NewManager(dir, "", &fakeNative{})
	if err != nil {
		t.Fatal("optional device state prevented chat startup", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if m.mobile.InitializationError() == nil {
		t.Fatal("corrupt device state must remain unavailable")
	}
	after, err := os.ReadFile(filepath.Join(legacyDir, "settings.json"))
	if err != nil || string(after) != string(broken) {
		t.Fatal("optional device state was overwritten", err)
	}
	if m.Config().Model == "" || m.memory == nil {
		t.Fatal("core chat configuration or memory unavailable")
	}
}

func TestMobileStorageFailureIsReportedWithoutPrivateContents(t *testing.T) {
	f := setup(t)
	privateFixture := []byte(`{"private_path":"synthetic-do-not-expose",`)
	writeLegacyMobileState(t, filepath.Join(f.dir, "mobile"), privateFixture, []byte(`{}`))
	f.login()
	status, body, _ := f.request("GET", "/api/mobile", nil, false, false)
	if status != 503 || strings.Contains(string(body), "synthetic-do-not-expose") {
		t.Fatal("device error not safely reported", status, string(body))
	}
	status, _, _ = f.request("GET", "/api/config", nil, false, false)
	if status != 200 {
		t.Fatal("optional device failure broke core configuration", status)
	}
}

func TestMobileStorageFailureDoesNotPreventAgentPause(t *testing.T) {
	f := setup(t)
	broken := []byte(`{"enabled":`)
	legacyDir := filepath.Join(f.dir, "mobile")
	writeLegacyMobileState(t, legacyDir, broken, []byte(`{}`))
	f.manager.mu.Lock()
	f.manager.config.Enabled = true
	f.manager.mu.Unlock()
	if err := f.manager.Toggle(false); err != nil {
		t.Fatal("unopened optional device state prevented Agent pause", err)
	}
	if f.manager.Config().Enabled {
		t.Fatal("Agent remained enabled")
	}
	after, err := os.ReadFile(filepath.Join(legacyDir, "settings.json"))
	if err != nil || string(after) != string(broken) {
		t.Fatal("pause changed unread device state", err)
	}
}

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
	if status, _, _ := f.request("GET", "/assets/plugin-packages.js", nil, false, false); status != 200 {
		t.Fatal("asset", status)
	}
	if status, _, _ := f.request("GET", "/assets/mobile.js", nil, false, false); status != 404 {
		t.Fatal("removed asset", status)
	}
}

func TestMobileMissingPlannerDoesNotQueueTaskOrConstructModel(t *testing.T) {
	f := setup(t)
	f.login()
	settings := f.manager.mobile.Settings()
	settings.Enabled = true
	settings.Devices = []string{"lab"}
	settings.Bridge.Command = "synthetic-must-not-be-executed"
	if err := f.manager.mobile.Save(settings); err != nil {
		t.Fatal(err)
	}
	status, body, _ := f.request("GET", "/api/mobile", nil, false, false)
	var view struct {
		PlannerAvailable bool   `json:"planner_available"`
		PlannerReason    string `json:"planner_reason"`
	}
	if status != 200 || json.Unmarshal(body, &view) != nil || view.PlannerAvailable || view.PlannerReason == "" {
		t.Fatal("missing planner not represented in management view", status, string(body))
	}
	status, _, _ = f.request("POST", "/api/mobile/tasks", map[string]string{"request_key": "planner-required", "device_id": "lab", "goal": "仅查看合成页面"}, true, true)
	if status != 409 || len(f.manager.mobile.List(nil)) != 0 {
		t.Fatal("missing planner queued or executed a device task", status)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "provider-key")); !os.IsNotExist(err) {
		t.Fatal("missing planner constructed a model", err)
	}
	status, _, _ = f.request("POST", "/api/mobile/stop", struct{}{}, true, true)
	if status != 200 {
		t.Fatal("missing planner prevented stopping existing work", status)
	}
}

func TestDeviceRecordingParametersAndSourceMemory(t *testing.T) {
	o := mobile.Observation{AllowedPackages: []string{"test.browser"}, Browsers: []string{"test.browser"}, WebHosts: []string{"example.com"}}
	if mobile.ValidStep(mobile.Step{Action: "capture"}, o) {
		t.Fatal("record duration omitted")
	}
	if !mobile.ValidStep(mobile.Step{Action: "capture", Arguments: map[string]any{"seconds": float64(4)}}, o) {
		t.Fatal("explicit recording duration rejected")
	}
	if mobile.ValidStep(mobile.Step{Action: "open_url"}, o) {
		t.Fatal("missing browser/url accepted")
	}
	if mobile.ValidStep(mobile.Step{Action: "open_url", Arguments: map[string]any{"package": "other.browser", "url": "https://example.com"}}, o) {
		t.Fatal("unlisted browser accepted")
	}
	if !mobile.ValidStep(mobile.Step{Action: "open_url", Arguments: map[string]any{"package": "test.browser", "url": "https://example.com"}}, o) {
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
