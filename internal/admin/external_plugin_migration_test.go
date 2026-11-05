package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/plugins"
)

func writeExternalMigrationMCP(t *testing.T, f *webFixture, enabled bool) {
	t.Helper()
	settings := mcpSettings{Enabled: enabled, WebSearchEnabled: true, WebFetchEnabled: true, MaxToolCalls: 3}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(f.dir, "mcp-config.json"), raw); err != nil {
		t.Fatal(err)
	}
	// setup(t) intentionally starts with an empty directory. Reopen the
	// repository after preparing the legacy fixture so the test does not model
	// an out-of-band edit that happened after a live repository had loaded it.
	repository, err := newExternalConfigurationRepository(f.dir, f.manager.native.Account())
	if err != nil {
		t.Fatal(err)
	}
	repository.openStore, repository.owned = f.manager.packageStore, false
	f.manager.plugins = newPluginLedgerForRepository(repository)
	f.manager.mu.Lock()
	f.manager.mcp = settings
	f.manager.mu.Unlock()
}

func writeExternalMigrationDeclaration(t *testing.T, f *webFixture, kind string) []byte {
	t.Helper()
	manifest := plugins.Manifest{ID: "legacy-external-" + kind, Name: "Synthetic legacy external", Version: "1"}
	switch kind {
	case "sync":
		manifest.Tools = []plugins.APITool{{Name: "lookup", Description: "Synthetic one-shot", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), URL: "https://private.example/v1/lookup", Method: "POST", Headers: map[string]string{"Authorization": "Bearer {{secret.TOKEN}}"}}}
	case "async":
		manifest.Tools = []plugins.APITool{{Name: "render", Description: "Synthetic background job", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), URL: "https://private.example/v1/jobs", Method: "POST", Job: &plugins.JobSpec{IDPath: "job_id"}, Poll: &plugins.PollSpec{URL: "https://private.example/v1/jobs/{{result.job_id}}", StatusPath: "state", DoneValues: []string{"done"}}}}
	case "poll-only":
		manifest.Tools = []plugins.APITool{{Name: "observe", Description: "Synthetic poll without a durable job", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), URL: "https://private.example/v1/jobs", Method: "POST", Poll: &plugins.PollSpec{URL: "https://private.example/v1/jobs/{{result.job_id}}", StatusPath: "state", DoneValues: []string{"done"}}}}
	case "mcp":
		manifest.MCP = &mcptools.ServerConfig{Transport: "http", URL: "https://private.example/mcp"}
	case "raw-stdio":
		manifest.MCP = &mcptools.ServerConfig{Transport: "stdio", Command: "private-command", Args: []string{"--private"}, Env: map[string]string{"TOKEN": "private"}}
	default:
		t.Fatal("unknown external declaration kind", kind)
	}
	entries := map[string]pluginEntry{manifest.ID: {Installed: plugins.Installed{Enabled: true, Manifest: manifest, Secrets: map[string]string{"TOKEN": "synthetic-external-secret"}}}}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(f.dir, "plugins.json"), raw); err != nil {
		t.Fatal(err)
	}
	repository, err := newExternalConfigurationRepository(f.dir, f.manager.native.Account())
	if err != nil {
		t.Fatal(err)
	}
	repository.openStore, repository.owned = f.manager.packageStore, false
	f.manager.plugins = newPluginLedgerForRepository(repository)
	return raw
}

func TestExternalToolsPluginMigrationAppliesHostRetainedSyncDeclarationDisabled(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	writeExternalMigrationMCP(t, f, true)
	original := writeExternalMigrationDeclaration(t, f, "sync")
	preview, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, externalHTTPPluginID)
	if !migrationCatalog()[externalHTTPPluginID].externalSync || !migrationCatalog()[externalHTTPPluginID].externalJob || step.Status != "ready_for_administrator_review" || !step.LegacyEnabled || !step.EligibleToEnable || step.ProposedEnabled || len(step.Values) != 0 || len(step.Secrets) != 0 || len(step.update.Values) != 0 || len(step.update.Secrets) != 0 || !migrationStringContains(step.UnmappedLegacy, "plugins.json:host_retained_declarations_and_credentials") || preview.Accounts[0].SourceHashes["plugins_json"] == "" {
		t.Fatal("invalid synchronous external migration proposal", step, preview.Accounts[0].SourceHashes)
	}
	encoded, _ := json.Marshal(preview)
	if strings.Contains(string(encoded), "synthetic-external-secret") || strings.Contains(string(encoded), "private.example") {
		t.Fatal("legacy external declaration leaked into public preview", string(encoded))
	}
	installed, configuration, record, err := f.manager.applyPluginMigration(context.Background(), "test", externalHTTPPluginID, step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions)
	if err != nil || installed.Enabled || record.State != "complete" || len(configuration.Values) != 0 || len(configuration.ConfiguredSecrets) != 0 {
		t.Fatal("sync external migration did not install empty disabled package", installed, configuration, record, err)
	}
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetEnabled(context.Background(), externalHTTPPluginID, installed.Revision, true); err != nil {
		t.Fatal("explicit administrator enable rejected migrated external package", err)
	}
	if current, readErr := os.ReadFile(filepath.Join(f.dir, "plugins.json")); readErr != nil || string(current) != string(original) {
		t.Fatal("migration changed the host-owned external ledger", readErr)
	}
}

func TestExternalToolsPluginMigrationAppliesHostRetainedJobDeclarationDisabled(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	writeExternalMigrationMCP(t, f, true)
	writeExternalMigrationDeclaration(t, f, "async")
	preview, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, externalHTTPPluginID)
	if step.Status != "ready_for_administrator_review" || !step.LegacyEnabled || !step.EligibleToEnable || step.ProposedEnabled || len(step.Values) != 0 || len(step.update.Values) != 0 || !migrationStringContains(step.UnmappedLegacy, "legacy_external_tasks:origin_and_configuration_proof_required") {
		t.Fatal("job/poll external declaration was not mapped to its ready capability", step)
	}
	installed, configuration, record, err := f.manager.applyPluginMigration(context.Background(), "test", externalHTTPPluginID, step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions)
	if err != nil || installed.Enabled || record.State != "complete" || len(configuration.Values) != 0 || len(configuration.ConfiguredSecrets) != 0 {
		t.Fatal("job external migration did not install empty disabled package", installed, configuration, record, err)
	}
}

func TestExternalToolsPluginMigrationAppliesPollOnlyAndMCPHTTPDeclarationsDisabled(t *testing.T) {
	useOfficialMigrationCatalog(t)
	for _, kind := range []string{"mcp", "poll-only"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			writeExternalMigrationMCP(t, f, true)
			writeExternalMigrationDeclaration(t, f, kind)
			preview, err := f.manager.pluginMigrationPreview(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			step := previewStep(t, preview, externalHTTPPluginID)
			if step.Status != "ready_for_administrator_review" || !step.LegacyEnabled || !step.EligibleToEnable || step.ProposedEnabled || len(step.Values) != 0 || len(step.update.Values) != 0 || len(step.update.Secrets) != 0 {
				t.Fatal("supported MCP or poll-only declaration was not mapped to its ready adapter", step)
			}
			installed, _, _, err := f.manager.applyPluginMigration(context.Background(), "test", externalHTTPPluginID, step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions)
			if err != nil || installed.Enabled {
				t.Fatal("supported external declaration did not install disabled", installed, err)
			}
		})
	}
}

func TestExternalToolsPluginMigrationMCPConfigHTTPAndRegisteredStdioAreSupported(t *testing.T) {
	useOfficialMigrationCatalog(t)
	for _, server := range []mcptools.ServerConfig{
		{ID: "remote", Transport: "streamable-http", URL: "https://private.example/mcp", Enabled: true},
		{ID: "registered", PluginID: "registered-runtime", Enabled: true},
	} {
		t.Run(server.ID, func(t *testing.T) {
			f := setup(t)
			settings := mcpSettings{Enabled: true, Servers: []mcptools.ServerConfig{server}}
			raw, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			if err = writePrivate(filepath.Join(f.dir, "mcp-config.json"), raw); err != nil {
				t.Fatal(err)
			}
			f.manager.mu.Lock()
			f.manager.mcp = settings
			f.manager.mu.Unlock()
			preview, err := f.manager.pluginMigrationPreview(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			step := previewStep(t, preview, externalHTTPPluginID)
			if step.Status != "ready_for_administrator_review" || !step.LegacyEnabled || !migrationStringContains(step.UnmappedLegacy, "mcp-config.json:host_retained_connections_and_credentials") {
				t.Fatal("MCP config was not mapped without exposing connection data", step)
			}
			encoded, _ := json.Marshal(preview)
			if strings.Contains(string(encoded), "private.example") || strings.Contains(string(encoded), "registered-runtime") {
				t.Fatal("MCP connection data leaked into preview", string(encoded))
			}
		})
	}
}

func TestExternalToolsPluginMigrationBlocksRawStdioButRetainsMixedWarnings(t *testing.T) {
	useOfficialMigrationCatalog(t)
	for _, kind := range []string{"raw-stdio", "sync-with-raw-stdio"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			writeExternalMigrationMCP(t, f, true)
			writeExternalMigrationDeclaration(t, f, "raw-stdio")
			if kind == "sync-with-raw-stdio" {
				entries := f.manager.plugins.repository.snapshot().Connections
				entries["legacy-external-sync"] = pluginEntry{Installed: plugins.Installed{
					Enabled: true, Manifest: plugins.Manifest{
						ID:      "legacy-external-sync",
						Name:    "sync",
						Version: "1",
						Tools:   []plugins.APITool{{Name: "lookup", URL: "https://private.example/v1", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}},
					},
				}}
				raw, err := json.Marshal(entries)
				if err != nil {
					t.Fatal(err)
				}
				if err = writePrivate(filepath.Join(f.dir, "plugins.json"), raw); err != nil {
					t.Fatal(err)
				}
				repository, err := newExternalConfigurationRepository(f.dir, f.manager.native.Account())
				if err != nil {
					t.Fatal(err)
				}
				repository.openStore, repository.owned = f.manager.packageStore, false
				f.manager.plugins = newPluginLedgerForRepository(repository)
			}
			preview, err := f.manager.pluginMigrationPreview(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			step := previewStep(t, preview, externalHTTPPluginID)
			if !migrationStringContains(step.UnmappedLegacy, "mcp_stdio_runtime_package_required") {
				t.Fatal("raw stdio requirement was silently omitted", step)
			}
			if kind == "raw-stdio" && (step.Status != "blocked" || !migrationStringContains(step.RequiredMissing, "no_matching_supported_external_tools")) {
				t.Fatal("raw stdio was treated as runnable", step)
			}
			if kind == "sync-with-raw-stdio" && (step.Status != "ready_for_administrator_review" || !step.LegacyEnabled) {
				t.Fatal("usable HTTP adapter was incorrectly blocked by retained raw stdio", step)
			}
		})
	}
}

func TestExternalToolsPluginMigrationRequiresDeclaredJobCapability(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	writeExternalMigrationMCP(t, f, true)
	writeExternalMigrationDeclaration(t, f, "async")
	packages := migrationCatalog()
	pkg := packages[externalHTTPPluginID]
	pkg.externalJob = false
	packages[externalHTTPPluginID] = pkg
	account, err := f.manager.pluginMigrationPreviewFor(context.Background(), packages)
	if err != nil {
		t.Fatal(err)
	}
	var step pluginMigrationStep
	for _, candidate := range account.Plugins {
		if candidate.Plugin == externalHTTPPluginID {
			step = candidate
			break
		}
	}
	if step.Status != "blocked" || step.LegacyEnabled || step.EligibleToEnable || !migrationStringContains(step.RequiredMissing, "no_matching_supported_external_tools") || !migrationStringContains(step.RequiredMissing, "official_external_job_adapter_not_declared_by_external_tools_plugin") {
		t.Fatal("job/poll declaration accepted without its reviewed capability", step)
	}
}

func TestExternalToolsPluginMigrationRejectsChangedLegacyLedger(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	writeExternalMigrationMCP(t, f, true)
	writeExternalMigrationDeclaration(t, f, "sync")
	preview, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, externalHTTPPluginID)
	entry, ok := f.manager.plugins.get("legacy-external-sync")
	if !ok {
		t.Fatal("synthetic declaration missing")
	}
	entry.Manifest.Description = "changed after administrator preview"
	if err = f.manager.plugins.save(entry); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = f.manager.applyPluginMigration(context.Background(), "test", externalHTTPPluginID, step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions); err == nil {
		t.Fatal("changed host ledger retained a stale migration approval")
	}
}

func TestExternalToolsPluginMigrationRejectsChangedMCPConfig(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	settings := mcpSettings{Enabled: true, Servers: []mcptools.ServerConfig{{ID: "remote", Transport: "http", URL: "https://private.example/mcp", Enabled: true}}}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(f.dir, "mcp-config.json"), raw); err != nil {
		t.Fatal(err)
	}
	f.manager.mu.Lock()
	f.manager.mcp = settings
	f.manager.mu.Unlock()
	preview, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, externalHTTPPluginID)
	settings.Servers[0].URL = "https://private.example/changed"
	raw, err = json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(f.dir, "mcp-config.json"), raw); err != nil {
		t.Fatal(err)
	}
	f.manager.mu.Lock()
	f.manager.mcp = settings
	f.manager.mu.Unlock()
	if _, _, _, err = f.manager.applyPluginMigration(context.Background(), "test", externalHTTPPluginID, step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions); err == nil {
		t.Fatal("changed MCP configuration retained a stale migration approval")
	}
}

func TestExternalToolsPluginMigrationUsesActiveHostStateInsteadOfEditedLegacyJSON(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	writeExternalMigrationMCP(t, f, true)
	writeExternalMigrationDeclaration(t, f, "sync")
	preview, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, externalHTTPPluginID)
	if _, _, _, err = f.manager.applyPluginMigration(context.Background(), "test", externalHTTPPluginID, step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions); err != nil {
		t.Fatal("activate external host state", err)
	}
	activeBefore, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	activeStep := previewStep(t, activeBefore, externalHTTPPluginID)
	if err = os.WriteFile(filepath.Join(f.dir, "plugins.json"), []byte(`{"edited":"legacy-only"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	afterLegacyEdit, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changed := previewStep(t, afterLegacyEdit, externalHTTPPluginID); changed.ApplySourceHash != activeStep.ApplySourceHash {
		t.Fatal("retained legacy JSON changed an active host-state approval", activeStep, changed)
	}
	if replayed, _, record, replayErr := f.manager.applyPluginMigration(context.Background(), "test", externalHTTPPluginID, step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions); replayErr != nil || replayed.Enabled || record.SourceHash != step.ApplySourceHash {
		t.Fatal("completed legacy-source migration was not safely idempotent after activation", replayed, record, replayErr)
	}
	settings := f.manager.plugins.repository.snapshot().Settings
	settings.MaxToolCalls++
	if err = f.manager.plugins.repository.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	afterActiveChange, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changed := previewStep(t, afterActiveChange, externalHTTPPluginID); changed.ApplySourceHash == activeStep.ApplySourceHash {
		t.Fatal("active SQLite configuration change retained stale approval", activeStep, changed)
	}
}

func TestExternalToolsPluginMigrationRetriesHostStateActivationAfterDisabledInstall(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	settings := mcpSettings{Enabled: true, WebSearchEnabled: true, WebFetchEnabled: true, MaxToolCalls: 3}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(f.dir, "mcp-config.json"), raw); err != nil {
		t.Fatal(err)
	}
	entries := map[string]pluginEntry{"legacy-external-sync": {Installed: plugins.Installed{Enabled: true, Manifest: plugins.Manifest{ID: "legacy-external-sync", Name: "sync", Version: "1", Tools: []plugins.APITool{{Name: "lookup", URL: "https://private.example/v1", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}}}}}}
	raw, err = json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(f.dir, "plugins.json"), raw); err != nil {
		t.Fatal(err)
	}
	repository, err := newExternalConfigurationRepository(f.dir, f.manager.native.Account())
	if err != nil {
		t.Fatal(err)
	}
	repository.owned = false
	f.manager.plugins = newPluginLedgerForRepository(repository)
	f.manager.mu.Lock()
	f.manager.mcp = settings
	f.manager.mu.Unlock()
	preview, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, externalHTTPPluginID)
	repository.openStore = func() (*pluginhost.Store, error) { return nil, errors.New("synthetic activation storage failure") }
	installed, _, _, err := f.manager.applyPluginMigration(context.Background(), "test", externalHTTPPluginID, step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions)
	if err == nil || installed.Enabled {
		t.Fatal("activation failure was reported as completed or enabled", installed, err)
	}
	if _, active := f.manager.externalConfigurationSources(); active {
		t.Fatal("failed activation made retained external configuration authoritative")
	}
	repository.openStore = f.manager.packageStore
	installed, _, _, err = f.manager.applyPluginMigration(context.Background(), "test", externalHTTPPluginID, step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions)
	if err != nil || installed.Enabled {
		t.Fatal("retry did not activate the existing disabled install", installed, err)
	}
	if _, active := f.manager.externalConfigurationSources(); !active {
		t.Fatal("successful retry did not activate host configuration")
	}
}
