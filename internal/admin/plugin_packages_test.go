package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func TestPluginPackageUpgradeReportsRequiredConfigurationMigration(t *testing.T) {
	f := setup(t)
	f.login()
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	source := readySpeechFixture(t)
	prepared, err := store.Prepare(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Install(t.Context(), prepared.Token, prepared.Digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.SaveConfiguration(t.Context(), "speech", pluginhost.ConfigUpdate{Values: map[string]json.RawMessage{"backend": json.RawMessage(`"synthetic-backend"`)}})
	if err != nil {
		t.Fatal(err)
	}
	schema := []byte(`{"type":"object","properties":{"backend":{"type":"integer"}},"additionalProperties":false}`)
	if err := os.WriteFile(filepath.Join(source, "config.schema.json"), schema, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := prepared.Package
	sum := sha256.Sum256(schema)
	manifest.Files["config.schema.json"] = hex.EncodeToString(sum[:])
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Prepare(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ := f.request("POST", "/api/plugin-packages/install", map[string]any{"token": updated.Token, "digest": updated.Digest, "permissions": []string{}, "confirm": true}, true, true)
	if code != 409 || !bytes.Contains(body, []byte(`"configuration_migration_required"`)) {
		t.Fatalf("upgrade response %d %s", code, body)
	}
	after, err := store.Installed(t.Context(), "speech")
	if err != nil || after.Digest != before.Digest || after.Revision != before.Revision {
		t.Fatal("failed upgrade changed installation", err)
	}
	retained, err := store.Configuration(t.Context(), "speech")
	if err != nil || retained.Revision != config.Revision || string(retained.Values["backend"]) != `"synthetic-backend"` {
		t.Fatal("failed upgrade changed configuration", err)
	}
}

// readySpeechFixture keeps generic package-admin lifecycle tests independent
// from the production speech plugin's evolving required cloud-TTS settings.
// Route/config-specific tests declare their own contract elsewhere.
func readySpeechFixture(t *testing.T) string {
	t.Helper()
	source := t.TempDir()
	if err := os.CopyFS(source, os.DirFS("../../plugins/speech")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(source, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest pluginhost.Package
	if err = json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		manifest.Files[name] = hex.EncodeToString(sum[:])
	}
	write("config.schema.json", `{"type":"object","properties":{"backend":{"type":"string"}},"additionalProperties":false}`)
	write("capabilities.json", `{"version":1,"capabilities":[{"id":"speech.synthesize","implementation":"ready","description":"Synthetic capability","permissions":["artifact.read","artifact.write","network","gpu","model.invoke"],"input_schema":{"type":"object"},"output_schema":{"type":"object"},"discoverable":true}]}`)
	manifest.Status = "ready"
	manifest.Permissions = []string{"artifact.read", "artifact.write", "network", "gpu", "model.invoke"}
	manifest.Runtime = pluginhost.Runtime{Transport: "mcp-http"}
	manifest.HTTPRoutes = nil
	b, _ = json.Marshal(manifest)
	if err = os.WriteFile(filepath.Join(source, "plugin.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestPluginPackageAdminApprovalAndAccountIsolation(t *testing.T) {
	f, accounts := setupAccounts(t)
	source := readySpeechFixture(t)
	input := map[string]any{"directory": source}
	if code, _, _ := f.request("POST", "/api/plugin-packages/prepare", input, true, false); code != 403 {
		t.Fatal("CSRF", code)
	}
	code, b, _ := f.request("POST", "/api/plugin-packages/prepare?account=second", input, true, true)
	if code != 200 {
		t.Fatalf("prepare %d %s", code, b)
	}
	var p pluginhost.Prepared
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	approval := map[string]any{"token": p.Token, "digest": p.Digest, "permissions": []string{"artifact.read"}, "confirm": false}
	if code, _, _ := f.request("POST", "/api/plugin-packages/install?account=second", approval, true, true); code != 400 {
		t.Fatal("missing confirmation", code)
	}
	approval["confirm"] = true
	if code, _, _ := f.request("POST", "/api/plugin-packages/install?account=test", approval, true, true); code != 409 {
		t.Fatal("cross-account approval", code)
	}
	if code, b, _ = f.request("POST", "/api/plugin-packages/install?account=second", approval, true, true); code != 201 {
		t.Fatalf("install %d %s", code, b)
	}
	code, b, _ = f.request("GET", "/api/plugin-packages?account=second", nil, true, false)
	var result struct {
		Installed []pluginhost.InstalledPackage `json:"installed"`
		Catalog   []pluginhost.Package          `json:"catalog"`
		Connected bool                          `json:"runtime_connected"`
	}
	if code != 200 || json.Unmarshal(b, &result) != nil || len(result.Installed) != 1 || len(result.Catalog) != 9 || result.Connected {
		t.Fatalf("invalid catalogue response %d", code)
	}
	if result.Installed[0].Enabled || accounts.lookup("second").manager.Config().Enabled {
		t.Fatal("install activated runtime")
	}
	configInput := map[string]any{"revision": 0, "values": map[string]string{"backend": "synthetic"}}
	if code, _, _ := f.request("POST", "/api/plugin-packages/speech/configuration?account=second", configInput, true, false); code != 403 {
		t.Fatal("configuration CSRF", code)
	}
	if code, _, _ := f.request("POST", "/api/plugin-packages/speech/configuration?account=second", configInput, true, true); code != 200 {
		t.Fatal("configuration save", code)
	}
	if code, _, _ := f.request("POST", "/api/plugin-packages/speech/configuration?account=second", configInput, true, true); code != 409 {
		t.Fatal("configuration stale write", code)
	}
	if code, _, _ := f.request("GET", "/api/plugin-packages/speech/jobs?account=second&limit=0", nil, true, false); code != 400 {
		t.Fatal("invalid job page size", code)
	}
	if code, b, _ := f.request("GET", "/api/plugin-packages/speech/jobs?account=second&limit=31", nil, true, false); code != 200 {
		t.Fatalf("job page %d %s", code, b)
	}
	// Main account has no installed package despite identical approval token.
	code, b, _ = f.request("GET", "/api/plugin-packages?account=test", nil, true, false)
	if code != 200 || json.Unmarshal(b, &result) != nil || len(result.Installed) != 0 {
		t.Fatal("registration leaked to primary")
	}
}

type packageTestExecutor struct{ starts, closes, calls int }

func (e *packageTestExecutor) Start(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
	e.starts++
	return e, nil
}
func (e *packageTestExecutor) Health(context.Context) error { return nil }
func (e *packageTestExecutor) Call(context.Context, pluginhost.Invocation, string, json.RawMessage) (json.RawMessage, error) {
	e.calls++
	return json.RawMessage(`{}`), nil
}
func (e *packageTestExecutor) Close() error { e.closes++; return nil }

func TestPluginPackageRuntimeAdminLifecycle(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	source := readySpeechFixture(t)
	prepared, err := store.Prepare(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	i, err := store.Install(ctx, prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	executor := &packageTestExecutor{}
	h, err := pluginruntime.NewController(store, executor)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime = h
	f.manager.packageRuntimeReady = true
	path := "/api/plugin-packages/speech/runtime"
	request := map[string]any{"action": "start", "revision": i.Revision}
	if code, _, _ := f.request("POST", path, request, false, false); code != 401 {
		t.Fatal("unauthenticated start", code)
	}
	f.login()
	if code, _, _ := f.request("POST", path, request, true, false); code != 403 {
		t.Fatal("missing CSRF", code)
	}
	if executor.starts != 0 {
		t.Fatal("unapproved process started")
	}
	if code, raw, _ := f.request("POST", path, request, true, true); code != 200 {
		t.Fatalf("start %d %s", code, raw)
	}
	if h.Status("speech").State != "running" || executor.starts != 1 {
		t.Fatal("fake lifecycle not connected")
	}
	if code, _, _ := f.request("POST", "/api/plugin-packages/speech/configuration", map[string]any{"revision": 0, "values": map[string]string{"backend": "synthetic"}}, true, true); code != 409 {
		t.Fatal("edited live config", code)
	}
	request["action"] = "stop"
	if code, _, _ := f.request("POST", path, request, true, true); code != 409 {
		t.Fatal("stale stop accepted", code)
	}
	i, err = store.Installed(ctx, "speech")
	if err != nil {
		t.Fatal(err)
	}
	request["revision"] = i.Revision
	if code, raw, _ := f.request("POST", path, request, true, true); code != 200 {
		t.Fatalf("stop %d %s", code, raw)
	}
	if h.Status("speech").State != "stopped" || executor.closes != 1 {
		t.Fatal("stop not connected")
	}
}

func TestPluginPackageCatalogRequiresLogin(t *testing.T) {
	f := setup(t)
	if code, _, _ := f.request("GET", "/api/plugin-packages", nil, false, false); code != 401 {
		t.Fatal("unprotected catalogue", code)
	}
	if _, err := os.Stat(filepath.Join(f.manager.dir, "plugin-host")); !os.IsNotExist(err) {
		t.Fatal("unauthenticated request created store")
	}
}
