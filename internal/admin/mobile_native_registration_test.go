package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"wechat-bot/internal/mcptools"
	mobile "wechat-bot/internal/devicespec"
	"wechat-bot/internal/pluginruntime"
)

// These are real profile/path seals, not an inferred native registration. The
// synthetic ADB file is never executed; IO integration injects a fixed runner.
func mobileDeviceIOResourceFixture(t *testing.T, m *Manager, settings mobile.Settings, python string) (pluginruntime.Options, mobile.Settings) {
	t.Helper()
	workspace := t.TempDir()
	state := filepath.Join(m.dir, "mobile")
	storage := filepath.Join(state, "synthetic-ledger")
	adbRoot := filepath.Join(workspace, "runtimes", "adb")
	catalog := filepath.Join(workspace, "plugins")
	entry := filepath.Join(workspace, "scripts", "mobile_bridge.py")
	for _, dir := range []string{state, storage, adbRoot, catalog, filepath.Dir(entry)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	adb := filepath.Join(adbRoot, "adb.exe")
	for _, name := range []string{adb, entry} {
		if err := os.WriteFile(name, []byte("synthetic fixture, never executed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if python == "" {
		var err error
		python, err = os.Executable()
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(settings.Devices) == 0 {
		settings.Devices = []string{"lab"}
	}
	if len(settings.Actions) == 0 {
		settings.Actions = []string{"tap", "launch", "open_url", "capture"}
	}
	devices := map[string]any{}
	for _, id := range settings.Devices {
		devices[id] = map[string]any{"serial": "synthetic-serial-" + id, "enabled": true, "packages": []string{"com.example.home", "com.browser"}, "actions": append([]string{"observe"}, settings.Actions...), "browsers": []string{"com.browser"}, "web_hosts": []string{"example.test"}}
	}
	profile := filepath.Join(state, "synthetic-profile.json")
	data, _ := json.Marshal(map[string]any{"adb": adb, "devices": devices})
	if err := os.WriteFile(profile, data, 0600); err != nil {
		t.Fatal(err)
	}
	settings.Bridge = mcptools.ServerConfig{Transport: "stdio", Args: []string{"-I", "-B", "-u", entry, "--config", profile, "--storage", storage}}
	options := pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), RuntimeRoots: []string{filepath.Dir(python)}, CatalogDirectory: catalog, DeviceToolsRoots: []string{adbRoot}}
	return options, settings
}

func TestMobileNativeCapabilitiesRequireInstalledPackageAndFixedIOResources(t *testing.T) {
	f := setup(t)
	if err := f.manager.mobileDeviceReady(t.Context()); err == nil || err.Error() != "device_plugin_required" {
		t.Fatal("native device capabilities reported ready without installed package", err)
	}
}
