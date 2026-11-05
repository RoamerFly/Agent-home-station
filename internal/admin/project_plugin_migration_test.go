package admin

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func projectMigrationSource(t *testing.T, f *webFixture, enabled bool, mode string) []byte {
	t.Helper()
	cfg := f.manager.agent.config()
	cfg.Enabled, cfg.Host.Mode = enabled, mode
	cfg.Host.Roots = []string{t.TempDir()}
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(f.dir, "agent-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestProjectPluginMigrationAppliesEmptyConfigurationDisabledAndCanEnable(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	original := projectMigrationSource(t, f, true, "full")
	preview, err := f.manager.pluginMigrationPreview(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, projectPluginID)
	if step.Status != "ready_for_administrator_review" || !step.LegacyEnabled || !step.EligibleToEnable || step.ProposedEnabled || len(step.Values) != 0 || len(step.update.Values) != 0 || len(step.Secrets) != 0 || len(step.update.Secrets) != 0 || len(step.Permissions) != 3 || preview.Accounts[0].SourceHashes["agent_config_json"] == "" {
		t.Fatal("invalid project migration proposal", step)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "plugin-host", "plugins.sqlite")); !os.IsNotExist(err) {
		t.Fatal("read-only project preview opened the store", err)
	}
	installed, config, record, err := f.manager.applyPluginMigration(t.Context(), "test", projectPluginID, step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions)
	if err != nil || installed.Enabled || record.State != "complete" || len(config.Values) != 0 || len(config.ConfiguredSecrets) != 0 {
		t.Fatal("empty-config project migration did not actually apply disabled", installed, config, record, err)
	}
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	again, repeatedConfig, repeatedRecord, err := f.manager.applyPluginMigration(t.Context(), "test", projectPluginID, step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions)
	if err != nil || again.Revision != installed.Revision || repeatedConfig.Revision != config.Revision || repeatedRecord.ID != record.ID {
		t.Fatal("empty-config migration was not idempotent", again, repeatedConfig, repeatedRecord, err)
	}
	if _, err := store.SetEnabled(t.Context(), projectPluginID, installed.Revision, true); err != nil {
		t.Fatal("explicit administrator enable rejected migrated empty configuration", err)
	}
	if current, err := os.ReadFile(filepath.Join(f.dir, "agent-config.json")); err != nil || !bytes.Equal(current, original) {
		t.Fatal("migration changed legacy roots or permissions", err)
	}
}

func TestProjectPluginMigrationDoesNotUpgradeReadFilesOrDisabledIntent(t *testing.T) {
	useOfficialMigrationCatalog(t)
	for _, tc := range []struct {
		name, mode string
		enabled    bool
	}{{"read", "read", true}, {"files", "files", true}, {"host disabled", "disabled", true}, {"agent disabled", "full", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			projectMigrationSource(t, f, tc.enabled, tc.mode)
			preview, err := f.manager.pluginMigrationPreview(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			step := previewStep(t, preview, projectPluginID)
			if step.Status != "blocked" || step.LegacyEnabled || step.EligibleToEnable || step.ProposedEnabled || !migrationStringContains(step.RequiredMissing, "explicit_project_permissions_review_required") || len(step.Values) != 0 || len(step.update.Values) != 0 {
				t.Fatal("restricted legacy intent silently became full plugin grants", step)
			}
			if _, _, _, err := f.manager.applyPluginMigration(t.Context(), "test", projectPluginID, step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions); err == nil {
				t.Fatal("restricted legacy mode applied without an explicit new authorization decision")
			}
			if _, err := os.Stat(filepath.Join(f.dir, "plugin-host", "plugins.sqlite")); !os.IsNotExist(err) {
				t.Fatal("blocked project migration opened the store", err)
			}
		})
	}
}

func TestProjectPluginMigrationRejectsStaleAgentConfiguration(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	projectMigrationSource(t, f, true, "full")
	preview, err := f.manager.pluginMigrationPreview(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, projectPluginID)
	// Even a roots-only change invalidates the reviewed source. Those roots
	// remain host-owned and are never installed as plugin configuration.
	projectMigrationSource(t, f, true, "full")
	if _, _, _, err := f.manager.applyPluginMigration(t.Context(), "test", projectPluginID, step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions); err == nil {
		t.Fatal("stale project proposal survived an agent source change")
	}
}
