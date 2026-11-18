package station

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
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
			data, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
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
	agent := localToolSet{access: &localWorkspaceAccess{home: f.handler.home}, projectTools: tools}
	for _, step := range []struct{ action, state string }{{"enable", "idle"}, {"start", "running"}, {"stop", "idle"}} {
		if result, err := agent.callStationLifecycle(t.Context(), "station_tool_"+step.action, json.RawMessage(`{"tool_id":"drop-in-echo"}`)); err != nil {
			t.Fatal(step.action, result, err)
		}
		if current := localProjectToolByID(t, f, id); current["state"] != step.state {
			t.Fatal(step.action, current)
		}
	}
}
