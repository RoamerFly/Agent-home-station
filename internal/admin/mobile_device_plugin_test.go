package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"wechat-bot/internal/devicebroker"
	"wechat-bot/internal/mobile"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

type fakeMobileDeviceBroker struct {
	calls    int
	raw      string
	err      error
	replies  map[string]string
	response func(string, json.RawMessage) (string, error)
	binding  string
	closeErr error
	closes   int
}

func (b *fakeMobileDeviceBroker) Call(_ context.Context, name string, raw json.RawMessage) (string, error) {
	b.calls++
	if !json.Valid(raw) {
		return "", errors.New("unexpected native request")
	}
	if b.response != nil {
		return b.response(name, raw)
	}
	if b.replies != nil {
		value, ok := b.replies[name]
		if !ok {
			return "", errors.New("unexpected native request")
		}
		return value, b.err
	}
	if name != "observe" {
		return "", errors.New("unexpected native request")
	}
	return b.raw, b.err
}
func (b *fakeMobileDeviceBroker) Close() error {
	b.closes++
	return b.closeErr
}
func (b *fakeMobileDeviceBroker) Binding() (string, error) {
	if b.binding == "" {
		return "synthetic-device-binding", nil
	}
	return b.binding, nil
}

func prepareSyntheticMobileDevicePackage(t *testing.T, store *pluginhost.Store) pluginhost.InstalledPackage {
	t.Helper()
	dir := t.TempDir()
	p := pluginhost.Package{SchemaVersion: 1, ID: mobileDevicePluginID, Name: "Synthetic mobile device", Version: "0.3.0-test", HostProtocol: 1, Status: "ready", Platforms: []string{runtime.GOOS}, Runtime: pluginhost.Runtime{Transport: "mcp-stdio", Entrypoint: "runtime/main.py"}, Permissions: []string{"device"}, Files: map[string]string{}}
	capabilities := make([]map[string]any, 0, len(mobileDeviceCapabilities))
	for _, id := range mobileDeviceCapabilities {
		capabilities = append(capabilities, map[string]any{"id": id, "implementation": "ready", "description": "synthetic", "permissions": []string{"device"}, "input_schema": map[string]any{"type": "object"}, "output_schema": map[string]any{"type": "object"}, "discoverable": true})
	}
	files := map[string]string{
		"config.schema.json": `{"type":"object","properties":{},"additionalProperties":false}`,
		"ui.schema.json":     `{}`,
		"capabilities.json":  string(mustJSON(map[string]any{"version": 1, "capabilities": capabilities})),
		"README.md":          "synthetic\n",
		"PERMISSIONS.md":     "synthetic\n",
		"MIGRATION.md":       "synthetic\n",
		"CHANGELOG.md":       "synthetic\n",
		"runtime/main.py":    "# synthetic\n",
	}
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		p.Files[name] = hex.EncodeToString(sum[:])
	}
	manifest, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "plugin.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	installed, err = store.SetEnabled(t.Context(), installed.Package.ID, installed.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	return installed
}

func mobileDevicePolicyFixture(t *testing.T) (*mobileDevicePluginBridge, *mobileDevicePluginPolicy, *fakeMobileDeviceBroker, pluginhost.Job, pluginhost.Capability) {
	t.Helper()
	f := setup(t)
	store, err := pluginhost.OpenStore(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	installed := prepareSyntheticMobileDevicePackage(t, store)
	registerSyntheticMobileNativeRuntime(t, f.manager, store, installed)
	f.manager.packages = store
	task := mobile.Task{ID: "task-device", RequestHash: "request-device", DeviceID: "phone-device", Goal: "检查页面", State: "running", Source: mobile.Source{Admin: true}, ObservationID: "obs-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	settings := mobile.Settings{Revision: 9, Enabled: true, Devices: []string{task.DeviceID}, Actions: []string{"tap", "launch", "open_url"}}
	f.manager.mobile = &mobilePlanDeviceStub{settings: settings, task: task}
	fake := &fakeMobileDeviceBroker{raw: `{"text":"{\"id\":\"obs-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"device_id\":\"phone-device\",\"package\":\"com.example.home\",\"width\":100,\"height\":200,\"allowed_packages\":[\"com.example.home\",\"com.browser\"],\"browsers\":[\"com.browser\"],\"web_hosts\":[\"example.test\"],\"nodes\":[],\"observed_at\":1.5,\"screenshot_sha256\":\"private\"}","media":[{"mime_type":"image/png","data_base64":"PRIVATE_PNG_BYTES"}]}`}
	bridge := &mobileDevicePluginBridge{nativeRegistration: f.manager.loadMobileNativeRegistration(), manager: f.manager, store: store, installed: installed, task: task, settings: settings, brokerOpener: func(context.Context, devicebroker.Options) (devicebroker.Bridge, error) { return fake, nil }, observations: map[string]mobile.Observation{}, artifacts: map[string]mobile.Artifact{}}
	policy, err := bridge.newPolicy(t.Context(), "observe", json.RawMessage(`{"device_id":"phone-device"}`))
	if err != nil {
		t.Fatal(err)
	}
	job := pluginhost.Job{Plugin: mobileDevicePluginID, Tool: policy.capability, Context: policy.invocation, Arguments: policy.arguments}
	return bridge, policy, fake, job, pluginhost.Capability{ID: policy.capability, Permissions: []string{"device"}}
}

func TestMobileDevicePluginObserveRetainsActionScopeFromNativeMetadata(t *testing.T) {
	bridge, policy, fake, job, cap := mobileDevicePolicyFixture(t)
	request, _ := json.Marshal(map[string]any{"operation": policy.capability, "source_version": policy.sourceVersion, "request": policy.request})
	if _, err := policy.Service(t.Context(), job, cap, "wechat/device_request", request); err != nil || fake.calls != 1 {
		t.Fatalf("initial native observation failed: %v", err)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"device_id":"phone-device","observation_id":"obs-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","request_key":"task-device-step-0","action":"launch","package":"com.browser"}`),
		json.RawMessage(`{"device_id":"phone-device","observation_id":"obs-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","request_key":"task-device-step-1","action":"open_url","package":"com.browser","url":"https://example.test/path"}`),
	} {
		if _, err := bridge.newPolicy(t.Context(), "act", raw); err != nil {
			t.Fatalf("native observation scope was lost for %s: %v", raw, err)
		}
	}
}

func TestMobileDevicePluginRetainsUnknownNativeOutcomeWithoutReplay(t *testing.T) {
	bridge, _, _, _, _ := mobileDevicePolicyFixture(t)
	request := map[string]any{"device_id": "phone-device", "observation_id": "obs-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "request_key": "task-device-step-0", "action": "tap", "x": float64(1), "y": float64(2)}
	bridge.observations["obs-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"] = mobile.Observation{ID: "obs-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DeviceID: "phone-device", Width: 100, Height: 200}
	receipt, err := bridge.safeReceipt(bridge.task, "device.act", "act", json.RawMessage(`{"state":"unknown","result":{}}`), request)
	if err != nil || mobileReceiptStatus("device.act", receipt) != "unknown" {
		t.Fatalf("unknown native outcome was rewritten or rejected: receipt=%s err=%v", receipt, err)
	}
}

func TestMobileDevicePluginPolicyProjectsPrivateObserveReply(t *testing.T) {
	_, policy, fake, job, cap := mobileDevicePolicyFixture(t)
	if strings.Contains(string(policy.arguments), "PRIVATE_PNG_BYTES") {
		t.Fatal("native screenshot bytes leaked into plugin arguments")
	}
	request, _ := json.Marshal(map[string]any{"operation": policy.capability, "source_version": policy.sourceVersion, "request": policy.request})
	result, err := policy.Service(t.Context(), job, cap, "wechat/device_request", request)
	if err != nil || fake.calls != 1 {
		t.Fatalf("mediated observation failed: %v calls=%d", err, fake.calls)
	}
	if strings.Contains(string(result), "PRIVATE_PNG_BYTES") || strings.Contains(string(result), "screenshot_sha256") || strings.Contains(string(result), "observed_at") {
		t.Fatalf("private native observation leaked in receipt: %s", result)
	}
	verified, err := policy.Validate(t.Context(), job, cap, result)
	if err != nil || strings.Contains(string(verified.Result), "PRIVATE_PNG_BYTES") || !strings.Contains(string(verified.Private), "PRIVATE_PNG_BYTES") {
		t.Fatalf("receipt/private split invalid: result=%s private=%s err=%v", verified.Result, verified.Private, err)
	}
	if _, err = policy.Service(t.Context(), job, cap, "wechat/device_request", request); err == nil || fake.calls != 1 {
		t.Fatal("completed/unknown native operation was replayed")
	}
}

func TestMobileDevicePluginPolicyRejectsRevokedSourceBeforeBroker(t *testing.T) {
	bridge, policy, fake, job, cap := mobileDevicePolicyFixture(t)
	stub := bridge.manager.mobile.(*mobilePlanDeviceStub)
	stub.task.Source.Admin = false
	request, _ := json.Marshal(map[string]any{"operation": policy.capability, "source_version": policy.sourceVersion, "request": policy.request})
	if _, err := policy.Service(t.Context(), job, cap, "wechat/device_request", request); err == nil || fake.calls != 0 {
		t.Fatalf("revoked source reached broker: err=%v calls=%d", err, fake.calls)
	}
}

func TestMobileDevicePluginPolicySnapshotRejectsTaskMutationBeforeBroker(t *testing.T) {
	bridge, policy, fake, job, cap := mobileDevicePolicyFixture(t)
	stub := bridge.manager.mobile.(*mobilePlanDeviceStub)
	stub.task.Steps++
	request, _ := json.Marshal(map[string]any{"operation": policy.capability, "source_version": policy.sourceVersion, "request": policy.request})
	if _, err := policy.Service(t.Context(), job, cap, "wechat/device_request", request); err == nil || fake.calls != 0 {
		t.Fatalf("changed durable task reached native broker: err=%v calls=%d", err, fake.calls)
	}
}

func TestMobileDevicePluginOldPolicyCannotFollowNewObservation(t *testing.T) {
	bridge, oldPolicy, fake, oldJob, cap := mobileDevicePolicyFixture(t)
	stub := bridge.manager.mobile.(*mobilePlanDeviceStub)
	stub.task.ObservationID = "obs-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := bridge.newPolicy(t.Context(), "observe", json.RawMessage(`{"device_id":"phone-device"}`)); err != nil {
		t.Fatalf("new operation could not bind the newer observation: %v", err)
	}
	request, _ := json.Marshal(map[string]any{"operation": oldPolicy.capability, "source_version": oldPolicy.sourceVersion, "request": oldPolicy.request})
	if _, err := oldPolicy.Service(t.Context(), oldJob, cap, "wechat/device_request", request); err == nil || fake.calls != 0 {
		t.Fatalf("old policy followed newer bridge state: err=%v calls=%d", err, fake.calls)
	}
}

func TestMobileDevicePluginPolicyRejectsBindingDriftBeforeBroker(t *testing.T) {
	_, policy, fake, job, cap := mobileDevicePolicyFixture(t)
	fake.binding = "changed-device-binding"
	request, _ := json.Marshal(map[string]any{"operation": policy.capability, "source_version": policy.sourceVersion, "request": policy.request})
	if _, err := policy.Service(t.Context(), job, cap, "wechat/device_request", request); err == nil || fake.calls != 0 {
		t.Fatalf("changed native binding reached broker: err=%v calls=%d", err, fake.calls)
	}
}

func TestMobileDevicePluginBridgeRetainsFailedNativeCloseForRetry(t *testing.T) {
	bridge, _, fake, _, _ := mobileDevicePolicyFixture(t)
	fake.closeErr = errors.New("synthetic close failure")
	if err := bridge.Close(); err == nil || fake.closes != 1 {
		t.Fatalf("first close did not report native failure: err=%v closes=%d", err, fake.closes)
	}
	if _, err := bridge.Call(t.Context(), "observe", json.RawMessage(`{"device_id":"phone-device"}`)); err == nil || fake.calls != 0 {
		t.Fatalf("closed bridge accepted a new call: err=%v calls=%d", err, fake.calls)
	}
	fake.closeErr = nil
	if err := bridge.Close(); err != nil || fake.closes != 2 {
		t.Fatalf("retained native close was not retried: err=%v closes=%d", err, fake.closes)
	}
	if bridge.broker != nil {
		t.Fatal("successful retry retained closed broker")
	}
}

// This opt-in test crosses the official v0.3 stdio package, the real
// Controller lifecycle and the production Bridge.Call method. The native
// device endpoint is a local fake only; no ADB, model, or WeChat is used.
func TestLiveMobileDevicePluginContract(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in isolated mobile device contract")
	}
	python, _ = filepath.Abs(python)
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	f := setup(t)
	store, err := pluginhost.OpenStore(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	prepared, err := store.Prepare(t.Context(), "../../plugins/mobile-control")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	if installed, err = store.SetEnabled(t.Context(), installed.Package.ID, installed.Revision, true); err != nil {
		t.Fatal(err)
	}
	f.manager.packages = store
	controller, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	task := mobile.Task{ID: "task-live-device", RequestHash: "request-live-device", DeviceID: "phone-device", Goal: "检查页面", State: "running", Source: mobile.Source{Admin: true}, ObservationID: "obs-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	settings := mobile.Settings{Revision: 5, Enabled: true, Devices: []string{task.DeviceID}, Actions: []string{"launch", "open_url", "capture"}}
	stub := &mobilePlanDeviceStub{settings: settings, task: task}
	f.manager.mobile = stub
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScL8WQAAAABJRU5ErkJggg=="
	observe := `{"text":"{\"id\":\"obs-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\"device_id\":\"phone-device\",\"package\":\"com.example.home\",\"width\":1,\"height\":1,\"allowed_packages\":[\"com.browser\"],\"browsers\":[\"com.browser\"],\"web_hosts\":[\"example.test\"],\"nodes\":[],\"observed_at\":1.25}","media":[{"mime_type":"image/png","data_base64":"` + png + `"}]}`
	fake := &fakeMobileDeviceBroker{response: func(name string, raw json.RawMessage) (string, error) {
		switch name {
		case "observe":
			return observe, nil
		case "act":
			var request struct {
				Action string `json:"action"`
			}
			if json.Unmarshal(raw, &request) != nil {
				return "", errors.New("invalid action request")
			}
			if request.Action == "capture" {
				return `{"state":"submitted","result":{"action":"capture","device_id":"phone-device","reobserve_required":true,"artifact":{"id":"clip-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","device_id":"phone-device","request_key":"task-live-device-step-2","kind":"screen_recording","mime":"video/mp4","bytes":32,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","requested_seconds":2,"audio":false,"original_media":false,"created_at":1.25}}}`, nil
			}
			return `{"state":"submitted","result":{"action":"` + request.Action + `","device_id":"phone-device","reobserve_required":false}}`, nil
		case "read_artifact":
			return `{"id":"clip-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","offset":0,"bytes":32,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","data_base64":"PRIVATE_FTYP_CHUNK","eof":true}`, nil
		default:
			return "", errors.New("unexpected native request")
		}
	}}
	workspace := t.TempDir()
	if err = os.MkdirAll(filepath.Join(workspace, "plugins"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECHAT_PLUGIN_RUNTIME_CONFIG", `{"python":"`+strings.ReplaceAll(python, "\\", "\\\\")+`","pythonw":"`+strings.ReplaceAll(filepath.Join(filepath.Dir(python), "pythonw.exe"), "\\", "\\\\")+`","windows_helper":"`+strings.ReplaceAll(helper, "\\", "\\\\")+`","runtime_roots":["`+strings.ReplaceAll(filepath.Dir(python), "\\", "\\\\")+`"],"catalog_directory":"`+strings.ReplaceAll(filepath.Join(workspace, "plugins"), "\\", "\\\\")+`","device_tools_roots":["`+strings.ReplaceAll(filepath.Dir(python), "\\", "\\\\")+`"]}`)
	registerSyntheticMobileNativeRuntime(t, f.manager, store, installed)
	bridgeRaw, err := f.manager.newMobilePluginBridge(t.Context(), task, settings)
	if err != nil {
		t.Fatal(err)
	}
	defer bridgeRaw.Close()
	bridgeRaw.(*mobileDevicePluginBridge).brokerOpener = func(context.Context, devicebroker.Options) (devicebroker.Bridge, error) {
		return fake, nil
	}
	call := func(name, raw string) string {
		out, e := bridgeRaw.Call(t.Context(), name, json.RawMessage(raw))
		if e != nil {
			page, _ := store.Jobs(t.Context(), mobileDevicePluginID, 0, 10)
			t.Fatalf("%s: %v native_calls=%d jobs=%#v", name, e, fake.calls, page.Items)
		}
		return out
	}
	call("observe", `{"device_id":"phone-device"}`)
	if fake.calls != 1 {
		t.Fatal("observe broker call missing")
	}
	stub.task.ObservationID = "obs-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	call("act", `{"device_id":"phone-device","observation_id":"obs-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","request_key":"task-live-device-step-0","action":"launch","package":"com.browser"}`)
	call("act", `{"device_id":"phone-device","observation_id":"obs-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","request_key":"task-live-device-step-1","action":"open_url","package":"com.browser","url":"https://example.test/"}`)
	call("act", `{"device_id":"phone-device","observation_id":"obs-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","request_key":"task-live-device-step-2","action":"capture","seconds":2}`)
	chunk := call("read_artifact", `{"device_id":"phone-device","artifact_id":"clip-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","request_key":"task-live-device-step-2","offset":0}`)
	if !strings.Contains(chunk, "PRIVATE_FTYP_CHUNK") {
		t.Fatal("host bridge did not retain private artifact bytes")
	}
	calls := fake.calls
	call("act", `{"device_id":"phone-device","observation_id":"obs-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","request_key":"task-live-device-step-0","action":"launch","package":"com.browser"}`)
	if fake.calls != calls {
		t.Fatal("same action key replayed native broker")
	}
	if _, err = bridgeRaw.Call(t.Context(), "read_artifact", json.RawMessage(`{"device_id":"phone-device","artifact_id":"clip-ffffffffffffffffffffffffffffffff","request_key":"task-live-device-step-2","offset":0}`)); err == nil || fake.calls != calls {
		t.Fatal("foreign artifact reached native broker")
	}
	stub.task.Source.Admin = false
	if _, err = bridgeRaw.Call(t.Context(), "observe", json.RawMessage(`{"device_id":"phone-device"}`)); err == nil || fake.calls != calls {
		t.Fatal("revoked source reached native broker")
	}
}
