package station

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"wechat-bot/internal/pluginruntime"
)

func TestExtractedToolPackageLifecycle(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows tool host")
	}
	runtimeRoot := os.Getenv("AHS_LOCAL_PROJECT_TOOL_RUNTIME_ROOT")
	if runtimeRoot == "" {
		t.Skip("set AHS_LOCAL_PROJECT_TOOL_RUNTIME_ROOT to the Windows release env")
	}
	base := t.TempDir()
	catalog := filepath.Join(base, "publictools")
	dir := filepath.Join(catalog, "template-python-stdio-回显")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f := newLocalHomeFixture(t, filepath.Join(base, "host"), filepath.Join(base, "workspace"))
	tools := f.handler.chat.projectTools
	python := filepath.Join(runtimeRoot, "windows-python-cli")
	tools.options = pluginruntime.Options{CatalogDirectory: catalog, Python: filepath.Join(python, "python.exe"), Pythonw: filepath.Join(python, "pythonw.exe"), RuntimeRoots: []string{python}, WindowsHelper: filepath.Join(runtimeRoot, "windows-agent-host", "windows_plugin_host.pyw")}
	f.login()
	if status, page := f.request("POST", "/api/local/home", map[string]string{"action": "initialize"}, true); status != http.StatusOK {
		t.Fatal(status, page)
	}
	// Simulate extracting a package after startup. The first list must not cache
	// the empty directory or initialize the package when it appears.
	source := filepath.Join("..", "..", "templates", "plugins", "python-stdio")
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{}
	schema := []byte(`{"type":"object","additionalProperties":false,"required":["api_key"],"properties":{"label":{"type":"string","default":"initial"},"api_key":{"type":"string","x-secret":true,"x-host-only":true}}}`)
	schemaDigest := sha256.Sum256(schema)
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if entry.Name() == "plugin.json" {
			if err = json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["id"] = "drop-in-echo"
			manifest["files"].(map[string]any)["config.schema.json"] = hex.EncodeToString(schemaDigest[:])
			data, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
		}
		if entry.Name() == "config.schema.json" {
			data = schema
		}
		if err = os.WriteFile(filepath.Join(dir, entry.Name()), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	const id = "drop-in-echo"
	route := "/api/local/project-tools/" + id
	discovered := localProjectToolByID(t, f, id)
	if discovered["external_package"] != true || discovered["catalog_available"] != true || discovered["installed"] != false {
		t.Fatal(discovered)
	}
	mutate := func(input map[string]any, want int) {
		t.Helper()
		if status, page := f.request("PUT", route, input, true); status != want {
			t.Fatalf("%v: %d %v", input["action"], status, page)
		}
	}
	mutate(map[string]any{"action": "install", "package_digest": discovered["package_digest"]}, 400)
	mutate(map[string]any{"action": "install", "package_digest": "stale", "approve_artifacts": true}, 409)
	mutate(map[string]any{"action": "install", "package_digest": discovered["package_digest"], "approve_artifacts": true}, 200)
	if installed := localProjectToolByID(t, f, id); installed["state"] != "disabled" {
		t.Fatal(installed)
	}
	configPath := filepath.Join(stationPublicToolsDir(f.handler.home.dataDir), id, externalToolConfigFile)
	if _, err = os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatal("configuration exists before enable", err)
	}
	installed := localProjectToolByID(t, f, id)
	mutate(map[string]any{"action": "enable", "revision": localProjectToolRevision(t, installed)}, 409)
	initial, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(initial, []byte(`"label": "initial"`)) || bytes.Contains(initial, []byte("api_key")) {
		t.Fatal("first-run template has wrong defaults")
	}
	userConfig := []byte(`{"values":{"label":"owner configuration"},"secrets":{"api_key":"synthetic-owner-secret"}}`)
	if err = os.WriteFile(configPath, userConfig, 0600); err != nil {
		t.Fatal(err)
	}
	agent := localToolSet{access: &localWorkspaceAccess{home: f.handler.home}, projectTools: tools}
	for _, step := range []struct{ action, state string }{{"enable", "idle"}, {"start", "running"}, {"stop", "idle"}} {
		if result, err := agent.callStationLifecycle(t.Context(), "station_tool_"+step.action, json.RawMessage(`{"tool_id":"drop-in-echo"}`)); err != nil {
			t.Fatal(step.action, result, err)
		}
		if current := localProjectToolByID(t, f, id); current["state"] != step.state {
			t.Fatal(step.action, current)
		}
	}
	record, err := f.handler.home.identity.Read()
	if err != nil {
		t.Fatal(err)
	}
	host, err := tools.forFish(record.FishID)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := host.store.ConfigurationForExecution(t.Context(), id)
	if err != nil || string(loaded.Values["label"]) != `"owner configuration"` || string(loaded.Values["api_key"]) != `"synthetic-owner-secret"` {
		t.Fatal("owner configuration was not loaded", err)
	}
	if raw, err := os.ReadFile(configPath); err != nil || !bytes.Equal(raw, userConfig) {
		t.Fatal("owner file was overwritten", err)
	}
	if raw, _ := json.Marshal(localProjectToolByID(t, f, id)); bytes.Contains(raw, []byte("synthetic-owner-secret")) {
		t.Fatal("configuration leaked through tool catalog")
	}
	mutate(map[string]any{"action": "start", "revision": localProjectToolRevision(t, localProjectToolByID(t, f, id))}, 200)
	unchanged, err := host.store.ConfigurationForExecution(t.Context(), id)
	if err != nil || unchanged.Revision != loaded.Revision {
		t.Fatal("no-op start changed configuration revision", err)
	}
	invalid := []byte(`{"values":{"label":13}}`)
	if err = os.WriteFile(configPath, invalid, 0600); err != nil {
		t.Fatal(err)
	}
	mutate(map[string]any{"action": "restart", "revision": localProjectToolRevision(t, localProjectToolByID(t, f, id))}, 409)
	retained, err := host.store.ConfigurationForExecution(t.Context(), id)
	if err != nil || retained.Revision != loaded.Revision {
		t.Fatal("invalid config replaced accepted revision", err)
	}
	if raw, err := os.ReadFile(configPath); err != nil || !bytes.Equal(raw, invalid) {
		t.Fatal("invalid config was replaced", err)
	}
	// A published tools directory automatically reloads a compatible updated
	// snapshot while preserving its settings and respecting in-flight work.
	if err = os.WriteFile(configPath, userConfig, 0600); err != nil {
		t.Fatal(err)
	}
	f.handler.home.toolPackages = catalog
	manifest["version"] = "0.1.2"
	nextManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "plugin.json"), nextManifest, 0600); err != nil {
		t.Fatal(err)
	}
	done := tools.markActive(id)
	waiting := localProjectToolByID(t, f, id)
	done()
	if waiting["update_available"] != true || !strings.Contains(waiting["reason"].(string), "在途任务") {
		t.Fatal("busy tool was reloaded", waiting)
	}
	upgraded := localProjectToolByID(t, f, id)
	if upgraded["update_available"] != false || upgraded["enabled"] != true {
		t.Fatal("updated tool did not reload", upgraded)
	}
	if raw, err := os.ReadFile(configPath); err != nil || !bytes.Equal(raw, userConfig) {
		t.Fatal("upgrade replaced owner settings", err)
	}
	blank := filepath.Join(catalog, "new-tool")
	if err = os.Mkdir(blank, 0700); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(blank, entry.Name()), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	automatic := localProjectToolByID(t, f, "example-echo")
	if automatic["installed"] != true || automatic["enabled"] != true || automatic["state"] != "idle" {
		t.Fatal("compatible package was not auto-registered", automatic)
	}

}
