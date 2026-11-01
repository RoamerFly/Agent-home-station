package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/mcptools"
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
	default:
		t.Fatal("unknown external declaration kind", kind)
	}
	entry := pluginEntry{Installed: plugins.Installed{Enabled: true, Manifest: manifest, Secrets: map[string]string{"TOKEN": "synthetic-external-secret"}}}
	if err := f.manager.plugins.save(entry); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(f.dir, "plugins.json"))
	if err != nil {
		t.Fatal(err)
	}
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

func TestExternalToolsPluginMigrationBlocksMCPOrPollOnlyDeclarations(t *testing.T) {
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
			if step.Status != "blocked" || step.LegacyEnabled || step.EligibleToEnable || step.ProposedEnabled || !migrationStringContains(step.RequiredMissing, "no_matching_supported_external_tools") || len(step.Values) != 0 || len(step.update.Values) != 0 || len(step.update.Secrets) != 0 {
				t.Fatal("MCP or poll-only declaration was silently represented as a supported external tool", step)
			}
			if _, _, _, err = f.manager.applyPluginMigration(context.Background(), "test", externalHTTPPluginID, step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions); err == nil {
				t.Fatal("blocked legacy declaration applied without a compatible adapter")
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
