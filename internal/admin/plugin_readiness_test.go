package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func TestPluginPackageReadinessReportsOnlyRequiredFieldLabels(t *testing.T) {
	f := setup(t)
	f.login()
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(t.Context(), requiredSecretSpeechFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions); err != nil {
		t.Fatal(err)
	}
	executor := &packageTestExecutor{}
	controller, err := pluginruntime.NewController(store, executor)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	code, body, _ := f.request("GET", "/api/plugin-packages", nil, true, false)
	if code != 200 || strings.Contains(string(body), "api-key-never-returned") {
		t.Fatalf("unsafe readiness overview %d %s", code, body)
	}
	var overview struct {
		Readiness map[string]pluginPackageReadiness `json:"readiness"`
	}
	if err = json.Unmarshal(body, &overview); err != nil {
		t.Fatal(err)
	}
	got := overview.Readiness["speech"]
	if got.State != "needs_configuration" || len(got.MissingRequiredFields) != 1 || got.MissingRequiredFields[0].ID != "api_key" || got.MissingRequiredFields[0].Label != "服务密钥" {
		t.Fatalf("unexpected overview readiness %#v", got)
	}
	code, body, _ = f.request("GET", "/api/plugin-packages/speech", nil, true, false)
	var detail struct {
		Readiness pluginPackageReadiness `json:"readiness"`
	}
	if code != 200 || json.Unmarshal(body, &detail) != nil || detail.Readiness.State != got.State || strings.Contains(string(body), "api-key-never-returned") {
		t.Fatalf("unsafe or inconsistent detail readiness %d %s", code, body)
	}
	if _, err = store.SaveConfiguration(t.Context(), "speech", pluginhost.ConfigUpdate{Secrets: map[string]string{"api_key": "api-key-never-returned"}}); err != nil {
		t.Fatal(err)
	}
	code, body, _ = f.request("GET", "/api/plugin-packages", nil, true, false)
	if code != 200 || strings.Contains(string(body), "api-key-never-returned") {
		t.Fatalf("configured secret leaked through readiness %d %s", code, body)
	}
	if err = json.Unmarshal(body, &overview); err != nil || overview.Readiness["speech"].State != "ready" || len(overview.Readiness["speech"].MissingRequiredFields) != 0 {
		t.Fatalf("configured secret was not recognized safely: %#v, %v", overview.Readiness["speech"], err)
	}
	item, err := store.Installed(t.Context(), "speech")
	if err != nil || item.Enabled || executor.starts != 0 {
		t.Fatalf("viewing a configured package changed its state: enabled=%v starts=%d err=%v", item.Enabled, executor.starts, err)
	}
}

func requiredSecretSpeechFixture(t *testing.T) string {
	t.Helper()
	source := readySpeechFixture(t)
	schema := []byte(`{"type":"object","properties":{"api_key":{"type":"string","title":"服务密钥","x-secret":true}},"required":["api_key"],"additionalProperties":false}`)
	if err := os.WriteFile(filepath.Join(source, "config.schema.json"), schema, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(source, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest pluginhost.Package
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(schema)
	manifest.Files["config.schema.json"] = hex.EncodeToString(sum[:])
	raw, _ = json.Marshal(manifest)
	if err = os.WriteFile(filepath.Join(source, "plugin.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return source
}

func dependencySpeechFixture(t *testing.T, dependency string) string {
	t.Helper()
	source := readySpeechFixture(t)
	raw, err := os.ReadFile(filepath.Join(source, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest pluginhost.Package
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Dependencies = []pluginhost.Dependency{{ID: dependency}}
	raw, _ = json.Marshal(manifest)
	if err = os.WriteFile(filepath.Join(source, "plugin.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestPluginPackageReadinessNeverStartsRuntimeAndUsesEnableValidator(t *testing.T) {
	f := setup(t)
	f.login()
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(t.Context(), readySpeechFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(t.Context(), prepared.Token, prepared.Digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveConfiguration(t.Context(), "speech", pluginhost.ConfigUpdate{Values: map[string]json.RawMessage{"backend": json.RawMessage(`"synthetic"`)}}); err != nil {
		t.Fatal(err)
	}
	code, body, _ := f.request("GET", "/api/plugin-packages/speech", nil, true, false)
	var detail struct {
		Readiness pluginPackageReadiness `json:"readiness"`
	}
	if code != 200 || json.Unmarshal(body, &detail) != nil {
		t.Fatalf("readiness %d %s", code, body)
	}
	if detail.Readiness.State != "not_enableable" || !hasReadinessCode(detail.Readiness.Reasons, "dependency_not_authorized") {
		t.Fatalf("permission readiness %#v", detail.Readiness)
	}
	current, err := store.Installed(t.Context(), installed.Package.ID)
	if err != nil || current.Enabled {
		t.Fatal("readiness changed enable state", err, current.Enabled)
	}
}

func TestPluginPackageReadinessPreflightsDependenciesWithoutStarting(t *testing.T) {
	f := setup(t)
	f.login()
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(t.Context(), dependencySpeechFixture(t, "missing-dependency"))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveConfiguration(t.Context(), installed.Package.ID, pluginhost.ConfigUpdate{Values: map[string]json.RawMessage{"backend": json.RawMessage(`"synthetic"`)}}); err != nil {
		t.Fatal(err)
	}
	executor := &packageTestExecutor{}
	controller, err := pluginruntime.NewController(store, executor)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime = controller
	f.manager.packageRuntimeReady = true
	code, body, _ := f.request("GET", "/api/plugin-packages/speech", nil, true, false)
	var detail struct {
		Readiness pluginPackageReadiness `json:"readiness"`
	}
	if code != 200 || json.Unmarshal(body, &detail) != nil || detail.Readiness.State == "ready" || !hasReadinessCode(detail.Readiness.Reasons, "dependency_missing") {
		t.Fatalf("dependency readiness %d %#v", code, detail.Readiness)
	}
	if executor.starts != 0 {
		t.Fatal("readiness preflight started a runtime", executor.starts)
	}
}

func hasReadinessCode(reasons []pluginReadinessReason, want string) bool {
	for _, reason := range reasons {
		if reason.Code == want {
			return true
		}
	}
	return false
}
