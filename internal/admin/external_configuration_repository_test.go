package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/plugins"
)

func externalConfigurationLegacyFixture(t *testing.T, dir string) (mcpSettings, pluginEntry) {
	t.Helper()
	settings := mcpDefaults()
	settings.Servers = []mcptools.ServerConfig{{ID: "synthetic", Transport: "http", URL: "https://synthetic.invalid/mcp", Enabled: false, Headers: map[string]string{"Authorization": "Bearer synthetic-mcp-secret"}}}
	entry := pluginEntry{Installed: plugins.Installed{Manifest: plugins.Manifest{ID: "synthetic", Name: "Synthetic external connection", Version: "1", Tools: []plugins.APITool{{Name: "echo", URL: "https://synthetic.invalid/echo", InputSchema: json.RawMessage(`{"type":"object"}`), Headers: map[string]string{"Authorization": "Bearer {{secret.KEY}}"}}}}, Secrets: map[string]string{"KEY": "synthetic-http-secret"}}}
	for name, value := range map[string]any{"mcp-config.json": settings, "plugins.json": map[string]pluginEntry{entry.Manifest.ID: entry}} {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return settings, entry
}

func externalConfigurationTestDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "plugin-host", "plugins.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestExternalConfigurationFreshGetRemainsOptionalAndReadOnly(t *testing.T) {
	f := setup(t)
	f.login()
	for _, path := range []string{"/api/plugins", "/api/mcp"} {
		if code, _, _ := f.request("GET", path, nil, false, false); code != 200 {
			t.Fatal("configuration read failed", path, code)
		}
	}
	if _, active := f.manager.externalConfigurationSources(); active {
		t.Fatal("GET activated external configuration")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "plugin-host")); !errors.Is(err, os.ErrNotExist) || f.manager.packages != nil {
		t.Fatal("chat-only configuration read opened an optional Store", err)
	}
	for _, name := range []string{"plugins.json", "mcp-config.json"} {
		if _, err := os.Stat(filepath.Join(f.dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("read created a legacy configuration", name, err)
		}
	}
}

func TestExternalConfigurationActivationIsAtomicIdempotentAndRetainsLegacy(t *testing.T) {
	dir := t.TempDir()
	wantSettings, wantEntry := externalConfigurationLegacyFixture(t, dir)
	legacy := map[string][]byte{}
	for _, name := range []string{"plugins.json", "mcp-config.json"} {
		legacy[name], _ = os.ReadFile(filepath.Join(dir, name))
	}
	r, err := newExternalConfigurationRepository(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "plugin-host")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy read created Store", err)
	}
	if err = r.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = r.Activate(t.Context()); err != nil || r.current.Revision != 1 {
		t.Fatal("activation was not idempotent", err, r.current.Revision)
	}
	settings := cloneMCP(wantSettings)
	settings.Enabled = false
	if err = r.SaveSettings(t.Context(), settings); err != nil {
		t.Fatal(err)
	}
	for name, raw := range legacy {
		after, _ := os.ReadFile(filepath.Join(dir, name))
		if !bytes.Equal(raw, after) {
			t.Fatal("SQLite commit rewrote a legacy file", name)
		}
		if err = os.WriteFile(filepath.Join(dir, name), []byte(`deliberately invalid retained source`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	reloaded, err := newExternalConfigurationRepository(dir, "test")
	if err != nil {
		t.Fatal("active SQLite state fell back to modified legacy JSON", err)
	}
	doc := reloaded.snapshot()
	if doc.Settings.Enabled || doc.Settings.Servers[0].Headers["Authorization"] != wantSettings.Servers[0].Headers["Authorization"] || doc.Connections[wantEntry.Manifest.ID].Secrets["KEY"] != wantEntry.Secrets["KEY"] || reloaded.current.SourceHash != r.current.SourceHash {
		t.Fatal("active settings, connections or import proof changed")
	}
	if _, err = newExternalConfigurationRepository(dir, "different-account"); err == nil {
		t.Fatal("another account opened the private connection namespace")
	}
	manager, err := NewManager(dir, "", &fakeNative{})
	if err != nil {
		t.Fatal("startup parsed obsolete corrupt JSON instead of active namespace", err)
	}
	defer manager.Close()
	if manager.mcp.Enabled || manager.plugins.repository.current.Revision != 2 {
		t.Fatal("startup did not load authoritative active configuration")
	}
}

func TestExternalConfigurationLargeConnectionIsChunkedWithoutRecordCap(t *testing.T) {
	dir := t.TempDir()
	_, entry := externalConfigurationLegacyFixture(t, dir)
	entry.Manifest.Tools = nil
	for index := 0; index < 16; index++ {
		schema, _ := json.Marshal(map[string]any{"type": "object", "x-synthetic-note": strings.Repeat("a", 60000)})
		body, _ := json.Marshal(map[string]string{"synthetic": strings.Repeat("b", 60000)})
		entry.Manifest.Tools = append(entry.Manifest.Tools, plugins.APITool{Name: fmt.Sprintf("tool_%02d", index), URL: "https://synthetic.invalid/echo", InputSchema: schema, BodyTemplate: body})
	}
	raw, _ := json.Marshal(map[string]pluginEntry{entry.Manifest.ID: entry})
	if len(raw) < 1800<<10 || len(raw) >= 2<<20 {
		t.Fatal("large connection fixture is outside the intended legacy range", len(raw))
	}
	if err := os.WriteFile(filepath.Join(dir, "plugins.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := newExternalConfigurationRepository(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Activate(t.Context()); err != nil {
		t.Fatal("near-2MiB connection was rejected by single-record boundary", err)
	}
	if len(r.current.Records) < 4 {
		t.Fatal("large private document was not split into safe records")
	}
	for _, record := range r.current.Records {
		if len(record) >= 1<<20 {
			t.Fatal("chunk exceeded HostState single-record resource boundary")
		}
	}
	reloaded, err := newExternalConfigurationRepository(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(reloaded.snapshot().Connections)
	if !bytes.Equal(raw, after) {
		t.Fatal("large connection was truncated or changed")
	}
	var configurations int
	if err = externalConfigurationTestDB(t, dir).QueryRow("SELECT count(*) FROM configurations WHERE plugin=?", externalHTTPPluginID).Scan(&configurations); err != nil || configurations != 0 {
		t.Fatal("host credentials entered plugin configuration", err)
	}
}

func TestExternalConfigurationConflictsAndDiskFailurePreserveMemory(t *testing.T) {
	dir := t.TempDir()
	externalConfigurationLegacyFixture(t, dir)
	r, err := newExternalConfigurationRepository(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	store, err := pluginhost.OpenStore(filepath.Join(dir, "plugin-host"), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r.openStore, r.owned = func() (*pluginhost.Store, error) { return store, nil }, false
	db := externalConfigurationTestDB(t, dir)
	if _, err = db.Exec(`CREATE TRIGGER external_fail_initialize BEFORE INSERT ON host_state_records BEGIN SELECT RAISE(ABORT, 'synthetic write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = r.Activate(t.Context()); err == nil || errors.Is(err, pluginhost.ErrRevision) {
		t.Fatal("initial write failure was hidden or became a conflict", err)
	}
	state, err := store.ReadHostState(t.Context(), externalHTTPPluginID, externalConfigurationNamespace)
	if err != nil || state.Revision != 0 || r.current.Revision != 0 || len(state.Records) != 0 {
		t.Fatal("failed initialize left a partial active namespace", err)
	}
	if _, err = db.Exec("DROP TRIGGER external_fail_initialize"); err != nil {
		t.Fatal(err)
	}
	if err = r.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	stale, err := newExternalConfigurationRepository(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	next := r.snapshot().Settings
	next.Enabled = false
	if err = r.SaveSettings(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(stale.snapshot())
	if err = stale.SaveSettings(t.Context(), next); !errors.Is(err, pluginhost.ErrRevision) {
		t.Fatal("stale writer bypassed namespace CAS", err)
	}
	after, _ := json.Marshal(stale.snapshot())
	if !bytes.Equal(before, after) || stale.current.Revision != 1 {
		t.Fatal("conflicted write changed in-memory configuration")
	}
	if _, err = db.Exec(`CREATE TRIGGER external_fail_update BEFORE UPDATE ON host_state_records BEGIN SELECT RAISE(ABORT, 'synthetic write failure'); END`); err != nil {
		t.Fatal(err)
	}
	ledger := newPluginLedgerForRepository(r)
	entry, _ := ledger.get("synthetic")
	entry.Secrets["KEY"] = "synthetic-replacement"
	unmodified, _ := ledger.get("synthetic")
	if unmodified.Secrets["KEY"] != "synthetic-http-secret" {
		t.Fatal("caller mutated a live credential through a returned map")
	}
	if err = ledger.save(entry); err == nil || errors.Is(err, pluginhost.ErrRevision) {
		t.Fatal("disk failure incorrectly published a credential update", err)
	}
	unmodified, _ = ledger.get("synthetic")
	if unmodified.Secrets["KEY"] != "synthetic-http-secret" || r.current.Revision != 2 {
		t.Fatal("failed commit altered the live connection or revision")
	}
	state, err = store.ReadHostState(t.Context(), externalHTTPPluginID, externalConfigurationNamespace)
	if err != nil || state.Revision != 2 {
		t.Fatal("failed update changed durable revision", err)
	}
}

func TestExternalConfigurationLegacyChangeAndCorruptActiveStateFailClosed(t *testing.T) {
	dir := t.TempDir()
	settings, _ := externalConfigurationLegacyFixture(t, dir)
	r, err := newExternalConfigurationRepository(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	settings.MaxToolCalls = 4
	raw, _ := json.Marshal(settings)
	if err = os.WriteFile(filepath.Join(dir, "mcp-config.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = r.Activate(t.Context()); !errors.Is(err, errExternalConfigurationChanged) {
		t.Fatal("changed legacy source was silently imported from stale memory", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "plugin-host")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source conflict created an optional Store", err)
	}
	r, err = newExternalConfigurationRepository(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = externalConfigurationTestDB(t, dir).Exec("UPDATE host_state_records SET payload=? WHERE plugin=? AND namespace=? AND key=?", []byte(`"tampered"`), externalHTTPPluginID, externalConfigurationNamespace, "document:000000"); err != nil {
		t.Fatal(err)
	}
	if _, err = newExternalConfigurationRepository(dir, "test"); err == nil {
		t.Fatal("corrupt active state fell back to otherwise valid retained legacy files")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "mcp-config.json"))
	if !bytes.Equal(raw, after) {
		t.Fatal("failed recovery overwrote the legacy source")
	}
}

func TestExternalConfigurationAPISavesPrivateStateAndRevokesSourceHash(t *testing.T) {
	f := setup(t)
	f.login()
	settings := mcpDefaults()
	settings.Servers = []mcptools.ServerConfig{{ID: "synthetic", Transport: "http", URL: "https://synthetic.invalid/mcp", Headers: map[string]string{"Authorization": "Bearer synthetic-secret"}}}
	before := externalMCPSourceHash(f.manager.mcp, f.manager.plugins.installed())
	if code, raw, _ := f.request("POST", "/api/mcp", settings, true, true); code != 200 || bytes.Contains(raw, []byte("synthetic-secret")) {
		t.Fatal("MCP private save leaked credentials or failed", code)
	}
	after := externalMCPSourceHash(f.manager.mcp, f.manager.plugins.installed())
	if before == after {
		t.Fatal("actual active connection change did not revoke source binding")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "mcp-config.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("new private save wrote legacy JSON", err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "mcp-config.json"), []byte(`obsolete retained data`), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadMCP(f.dir)
	if err != nil || externalMCPSourceHash(loaded, f.manager.plugins.installed()) != after {
		t.Fatal("legacy edit replaced active authority", err)
	}
	state, err := f.manager.packages.ReadHostState(context.Background(), externalHTTPPluginID, externalConfigurationNamespace)
	if err != nil || state.Revision != 1 {
		t.Fatal("explicit API save did not create one atomic namespace", err)
	}
}

func TestExternalConfigurationAdminReloadRecoversFromCASConflict(t *testing.T) {
	f := setup(t)
	f.login()
	settings := mcpDefaults()
	if code, _, _ := f.request("POST", "/api/mcp", settings, true, true); code != 200 {
		t.Fatal("initial save", code)
	}
	peer, err := newExternalConfigurationRepository(f.dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	next := peer.snapshot().Settings
	next.Enabled = false
	if err = peer.SaveSettings(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	settings.WebFetchEnabled = false
	if code, _, _ := f.request("POST", "/api/mcp", settings, true, true); code != 409 {
		t.Fatal("stale API save bypassed CAS", code)
	}
	if !f.manager.mcp.Enabled {
		t.Fatal("failed stale write changed memory before reload")
	}
	code, body, _ := f.request("GET", "/api/mcp", nil, false, false)
	var reloaded mcpSettings
	if code != 200 || json.Unmarshal(body, &reloaded) != nil || reloaded.Enabled {
		t.Fatal("administrator reload did not read committed active version", code)
	}
	reloaded.WebFetchEnabled = false
	if code, _, _ := f.request("POST", "/api/mcp", reloaded, true, true); code != 200 {
		t.Fatal("reload could not resume saving", code)
	}
	if f.manager.plugins.repository.current.Revision != 3 {
		t.Fatal("reload/save revision drift")
	}
}

func TestExternalConfigurationPluginMCPCredentialsAreRedactedAndRoundTrip(t *testing.T) {
	f := setup(t)
	f.login()
	entry := pluginEntry{Installed: plugins.Installed{Manifest: plugins.Manifest{ID: "literal_mcp", Name: "Synthetic literal MCP", Version: "1", MCP: &mcptools.ServerConfig{Transport: "http", URL: "https://synthetic.invalid/mcp", Env: map[string]string{"TOKEN": "synthetic-literal-env"}, Headers: map[string]string{"X-Provider-Token": "Bearer synthetic-literal-header"}}}, Secrets: map[string]string{"KEY": "synthetic-private-registry"}}}
	if err := f.manager.plugins.save(entry); err != nil {
		t.Fatal(err)
	}
	directView := redactedPluginManifest(entry.Manifest)
	if len(directView.MCP.Env) != 0 || len(directView.MCP.Headers) != 0 || entry.Manifest.MCP.Env["TOKEN"] != "synthetic-literal-env" {
		t.Fatal("manifest redaction mutated its source")
	}
	code, body, _ := f.request("GET", "/api/plugins", nil, false, false)
	if code != 200 || bytes.Contains(body, []byte("synthetic-literal-env")) || bytes.Contains(body, []byte("synthetic-literal-header")) || bytes.Contains(body, []byte("synthetic-private-registry")) {
		t.Fatal("MCP manifest credentials escaped configuration GET", code)
	}
	var view struct {
		Installed []struct {
			Manifest plugins.Manifest `json:"manifest"`
		} `json:"installed"`
	}
	if json.Unmarshal(body, &view) != nil || len(view.Installed) != 1 || view.Installed[0].Manifest.MCP == nil {
		t.Fatal("missing redacted manifest")
	}
	if code, _, _ := f.request("POST", "/api/plugins/literal_mcp", map[string]any{"manifest": view.Installed[0].Manifest, "enabled": false}, true, true); code != 200 {
		t.Fatal("same-target redacted roundtrip failed", code)
	}
	stored, _ := f.manager.plugins.get("literal_mcp")
	if stored.Manifest.MCP.Env["TOKEN"] != "synthetic-literal-env" || stored.Manifest.MCP.Headers["X-Provider-Token"] != "Bearer synthetic-literal-header" || stored.Secrets["KEY"] != "synthetic-private-registry" {
		t.Fatal("redacted roundtrip lost host credentials")
	}
}

func TestExternalConfigurationPluginMCPChangedTargetNeverInheritsCredentials(t *testing.T) {
	f := setup(t)
	f.login()
	entry := pluginEntry{Installed: plugins.Installed{Manifest: plugins.Manifest{ID: "literal_mcp", Name: "Synthetic literal MCP", Version: "1", MCP: &mcptools.ServerConfig{Transport: "http", URL: "https://synthetic.invalid/mcp", Env: map[string]string{"TOKEN": "synthetic-old-env"}, Headers: map[string]string{"X-Provider-Token": "Bearer synthetic-old-header"}}}}}
	if err := f.manager.plugins.save(entry); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*mcptools.ServerConfig){
		func(c *mcptools.ServerConfig) { c.URL = "https://different.invalid/mcp" },
		func(c *mcptools.ServerConfig) { c.Command = "different-command" },
		func(c *mcptools.ServerConfig) { c.Args = []string{"different-argument"} },
		func(c *mcptools.ServerConfig) { c.PluginID = "different-runtime" },
	} {
		candidate := redactedPluginManifest(entry.Manifest)
		change(candidate.MCP)
		if code, body, _ := f.request("POST", "/api/plugins/literal_mcp", map[string]any{"manifest": candidate, "enabled": false}, true, true); code != 400 || bytes.Contains(body, []byte("synthetic-old-")) {
			t.Fatal("changed target reused host credential or leaked a value", code)
		}
	}
	candidate := redactedPluginManifest(entry.Manifest)
	candidate.MCP.URL = "https://different.invalid/mcp"
	candidate.MCP.Env = map[string]string{"TOKEN": "synthetic-new-env"}
	if code, _, _ := f.request("POST", "/api/plugins/literal_mcp", map[string]any{"manifest": candidate, "enabled": false}, true, true); code != 200 {
		t.Fatal("explicit replacement credentials could not save a different target", code)
	}
	stored, _ := f.manager.plugins.get("literal_mcp")
	if stored.Manifest.MCP.Env["TOKEN"] != "synthetic-new-env" || len(stored.Manifest.MCP.Headers) != 0 {
		t.Fatal("old header inherited across target change")
	}
}
