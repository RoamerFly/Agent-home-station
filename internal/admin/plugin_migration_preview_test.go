package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	catalog "wechat-bot/plugins"
)

func useOfficialMigrationCatalog(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs("../../plugins")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECHAT_PLUGIN_RUNTIME_CONFIG", `{"catalog_directory":`+strconv.Quote(root)+`}`)
}

func previewStep(t *testing.T, p pluginMigrationPreview, id string) pluginMigrationStep {
	t.Helper()
	for _, step := range p.Accounts[0].Plugins {
		if step.Plugin == id {
			return step
		}
	}
	t.Fatal("migration step missing", id)
	return pluginMigrationStep{}
}

func TestPluginMigrationPreviewRedactsSecretsAndPreservesDeclaredDeepSeekBackend(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	f.manager.mu.Lock()
	f.manager.config.Provider, f.manager.config.BaseURL, f.manager.config.APIKey, f.manager.config.Model = "deepseek", "https://api.deepseek.com", "synthetic-private-key", "vision-model"
	f.manager.mcp = mcpSettings{Enabled: true, WebSearchEnabled: true, WebFetchEnabled: true}
	f.manager.mu.Unlock()
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.TTSEnabled, legacySpeechFixture.ASREnabled, legacySpeechFixture.ASRBackend, legacySpeechFixture.ASRLocalModel, legacySpeechFixture.Key = true, true, "local_then_bailian", "synthetic-asr", "synthetic-private-key"
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	if err := writePrivate(f.dir+"/channels-config.json", []byte(`{"enabled":true,"cookie":"synthetic-cookie","object_bridge_url":"http://127.0.0.1:1234"}`)); err != nil {
		t.Fatal(err)
	}
	p, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil || len(p.Accounts) != 1 || !p.ReadOnly {
		t.Fatal(err, p)
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "synthetic-private-key") || strings.Contains(string(raw), "synthetic-cookie") {
		t.Fatal("secret leaked in preview", string(raw))
	}
	web := previewStep(t, p, "web-search")
	if string(web.Values["search_backend"]) != `"deepseek"` || !web.Secrets["search_key"].Present || web.update.Secrets["search_key"] != "synthetic-private-key" {
		t.Fatal("declared DeepSeek backend was not bound", web)
	}
	if string(web.Values["legacy_web_search_enabled"]) != "true" || string(web.Values["legacy_web_fetch_enabled"]) != "true" || string(web.update.Values["search_backend"]) != `"deepseek"` || len(web.update.Values) != 1 {
		t.Fatal("legacy web switches escaped the preview-only audit boundary", web)
	}
	contract, err := catalog.ContractFor("web-search")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile("../../plugins/web-search/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(manifest)
	wantDigest := hex.EncodeToString(sum[:])
	if contract.ManifestSHA256 != wantDigest || web.Package.ExpectedDigest != wantDigest || web.Package.CatalogSHA256 != wantDigest || !migrationStringContains(web.Package.Permissions, "network") || !migrationStringContains(web.Permissions, "network") {
		t.Fatal("preview does not expose the exact catalog package contract", web.Package)
	}
	if web.Status != "ready_for_administrator_review" || !web.EligibleToEnable || len(web.RequiredMissing) != 0 {
		t.Fatal("declared backend should not be blocked by a fabricated package digest requirement", web)
	}
	if !strings.Contains(string(raw), "legacy_local_then_bailian_requires_explicit_choice") || !strings.Contains(string(raw), "object_bridge_url") || strings.Contains(string(raw), "official_deepseek_search_adapter_not_declared_by_web-search_plugin") {
		t.Fatal("unexpected migration boundary", string(raw))
	}
	for _, id := range []string{"media-understanding", "speech", "web-search", "share-reader"} {
		step := previewStep(t, p, id)
		if step.ProposedEnabled || step.ApplySourceHash == "" {
			t.Fatal("unsafe or incomplete step", id, step)
		}
	}
}

func TestWebMigrationAppliesOnlyDeclaredConfigurationAndCanEnable(t *testing.T) {
	useOfficialMigrationCatalog(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name                        string
		searchEnabled, fetchEnabled bool
		deepSeek                    bool
		backend                     string
	}{
		{name: "public fetch only", fetchEnabled: true, backend: "public"},
		{name: "official deepseek search only", searchEnabled: true, deepSeek: true, backend: "deepseek"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			f.manager.mu.Lock()
			f.manager.mcp = mcpSettings{Enabled: true, WebSearchEnabled: tc.searchEnabled, WebFetchEnabled: tc.fetchEnabled, MaxToolCalls: 3}
			if tc.deepSeek {
				f.manager.config.Provider = "deepseek"
				f.manager.config.BaseURL = "https://api.deepseek.com"
				f.manager.config.APIKey = "synthetic-search-key"
			}
			f.manager.mu.Unlock()

			preview, err := f.manager.pluginMigrationPreview(ctx)
			if err != nil {
				t.Fatal(err)
			}
			step := previewStep(t, preview, "web-search")
			if step.Status != "ready_for_administrator_review" || !step.EligibleToEnable || string(step.Values["legacy_web_search_enabled"]) != strconv.FormatBool(tc.searchEnabled) || string(step.Values["legacy_web_fetch_enabled"]) != strconv.FormatBool(tc.fetchEnabled) || string(step.Values["search_backend"]) != strconv.Quote(tc.backend) {
				t.Fatalf("unexpected web preview: %#v", step)
			}
			if len(step.update.Values) != 1 || string(step.update.Values["search_backend"]) != strconv.Quote(tc.backend) {
				t.Fatalf("non-schema legacy fields entered apply configuration: %#v", step.update.Values)
			}
			if tc.deepSeek != (step.update.Secrets["search_key"] == "synthetic-search-key") {
				t.Fatalf("unexpected key mapping: %#v", step.update.Secrets)
			}

			installed, config, record, err := f.manager.applyPluginMigration(ctx, "test", "web-search", step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions)
			if err != nil || installed.Enabled || record.State != "complete" || len(config.Values) != 1 || string(config.Values["search_backend"]) != strconv.Quote(tc.backend) {
				t.Fatalf("actual web migration failed: installed=%+v config=%+v record=%+v err=%v", installed, config, record, err)
			}
			store, err := f.manager.packageStore()
			if err != nil {
				t.Fatal(err)
			}
			// A replay before enable is an idempotent migration resume, not a
			// second install or a changed configuration.
			again, againConfig, againRecord, err := f.manager.applyPluginMigration(ctx, "test", "web-search", step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions)
			if err != nil || again.Revision != installed.Revision || againConfig.Revision != config.Revision || againRecord.ID != record.ID {
				t.Fatalf("identical migration did not resume: %+v %+v %+v %v", again, againConfig, againRecord, err)
			}
			if _, err = store.SetEnabled(ctx, "web-search", installed.Revision, true); err != nil {
				t.Fatalf("valid migrated web configuration could not enable: %v", err)
			}
			execution, err := store.ConfigurationForExecution(ctx, "web-search")
			if err != nil || string(execution.Values["search_backend"]) != strconv.Quote(tc.backend) || tc.deepSeek != (string(execution.Values["search_key"]) == `"synthetic-search-key"`) {
				t.Fatalf("execution configuration mismatch: %#v %v", execution, err)
			}
		})
	}
}

func TestWebMigrationRejectsChangedLegacySource(t *testing.T) {
	useOfficialMigrationCatalog(t)
	ctx := context.Background()
	f := setup(t)
	f.manager.mu.Lock()
	f.manager.mcp = mcpSettings{Enabled: true, WebSearchEnabled: true, WebFetchEnabled: false, MaxToolCalls: 3}
	f.manager.mu.Unlock()
	preview, err := f.manager.pluginMigrationPreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, preview, "web-search")
	if err = writePrivate(filepath.Join(f.dir, "mcp-config.json"), []byte(`{"enabled":true,"web_search_enabled":false,"web_fetch_enabled":true,"max_tool_calls":3,"servers":[]}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = f.manager.applyPluginMigration(ctx, "test", "web-search", step.ApplySourceHash, step.Package.ExpectedDigest, step.Permissions); err == nil {
		t.Fatal("migration accepted a plan after its mcp source hash changed")
	}
}

func TestPluginMigrationPreviewBlocksPaidSearchWithoutDeclaredBackend(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	f.manager.mu.Lock()
	f.manager.config.Provider, f.manager.config.BaseURL, f.manager.config.APIKey = "deepseek", "https://api.deepseek.com", "synthetic-private-key"
	f.manager.mu.Unlock()
	packages := migrationCatalog()
	packages["web-search"] = pluginMigrationPackage{Version: "0.2.0", Status: "ready"} // old contract: no fixed backend declaration
	account, err := f.manager.pluginMigrationPreviewFor(context.Background(), packages)
	if err != nil {
		t.Fatal(err)
	}
	var web pluginMigrationStep
	for _, step := range account.Plugins {
		if step.Plugin == "web-search" {
			web = step
		}
	}
	if !strings.Contains(strings.Join(web.RequiredMissing, ","), "official_deepseek_search_adapter_not_declared_by_web-search_plugin") || len(web.Secrets) != 0 || web.update.Secrets != nil {
		t.Fatal("paid search silently changed provider", web)
	}
}

func TestPluginMigrationPreviewLocalProfileAndSourceHashChange(t *testing.T) {
	useOfficialMigrationCatalog(t)
	f := setup(t)
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.ASREnabled, legacySpeechFixture.ASRBackend, legacySpeechFixture.ASRLocalModel = true, "local", "sensevoice-synthetic"
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	first, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	media := previewStep(t, first, "media-understanding")
	if media.ProfileID != "sensevoice-synthetic" || string(media.Values["local_asr_profile_id"]) != `"sensevoice-synthetic"` || string(media.update.Values["local_asr_profile_id"]) != `"sensevoice-synthetic"` || !strings.Contains(strings.Join(media.RequiredMissing, ","), "registered_local_asr_profile:sensevoice-synthetic") {
		t.Fatal(media)
	}
	if err = writePrivate(f.dir+"/mcp-config.json", []byte(`{"enabled":false,"web_search_enabled":false,"web_fetch_enabled":false,"max_tool_calls":3,"servers":[]}`)); err != nil {
		t.Fatal(err)
	}
	second, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Accounts[0].SourceHashes["mcp_config_json"] == second.Accounts[0].SourceHashes["mcp_config_json"] || previewStep(t, first, "web-search").ApplySourceHash == previewStep(t, second, "web-search").ApplySourceHash {
		t.Fatal("source change did not invalidate plan")
	}
}

func TestPluginMigrationPreviewMapsDeclaredLoopbackBridgeButBlocksImplicitCookieFallback(t *testing.T) {
	f := setup(t)
	if err := writePrivate(f.dir+"/channels-config.json", []byte(`{"enabled":true,"cookie":"synthetic-cookie","object_bridge_url":"http://127.0.0.1:3210/"}`)); err != nil {
		t.Fatal(err)
	}
	packages := migrationCatalog()
	if !packages["share-reader"].objectBridge {
		t.Fatal("share reader bridge declaration was not discovered")
	}
	account, err := f.manager.pluginMigrationPreviewFor(context.Background(), packages)
	if err != nil {
		t.Fatal(err)
	}
	var share pluginMigrationStep
	for _, step := range account.Plugins {
		if step.Plugin == "share-reader" {
			share = step
		}
	}
	if string(share.Values["object_bridge_url"]) != `"http://127.0.0.1:3210"` || string(share.update.Values["object_bridge_url"]) != `"http://127.0.0.1:3210"` {
		t.Fatal("declared bridge was not carried into host configuration", share)
	}
	if !strings.Contains(strings.Join(share.RequiredMissing, ","), "explicit_bridge_fallback_policy_needed") {
		t.Fatal("legacy bridge/cookie fallback changed without an explicit decision", share)
	}
}

func TestPluginMigrationPreviewRejectsNonLoopbackBridge(t *testing.T) {
	f := setup(t)
	if err := writePrivate(f.dir+"/channels-config.json", []byte(`{"enabled":true,"object_bridge_url":"https://bridge.example"}`)); err != nil {
		t.Fatal(err)
	}
	packages := migrationCatalog()
	if !packages["share-reader"].objectBridge {
		t.Fatal("share reader bridge declaration was not discovered")
	}
	account, err := f.manager.pluginMigrationPreviewFor(context.Background(), packages)
	if err != nil {
		t.Fatal(err)
	}
	var share pluginMigrationStep
	for _, step := range account.Plugins {
		if step.Plugin == "share-reader" {
			share = step
		}
	}
	if _, ok := share.Values["object_bridge_url"]; ok || !strings.Contains(strings.Join(share.RequiredMissing, ","), "object_bridge_url_invalid") {
		t.Fatal("non-loopback bridge reached plugin configuration", share)
	}
}

func TestPluginMigrationPreviewBlocksMissingOfficialSourceWithoutStoreWrite(t *testing.T) {
	t.Setenv("WECHAT_PLUGIN_RUNTIME_CONFIG", "")
	f := setup(t)
	p, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(previewStep(t, p, "speech").RequiredMissing, ","), "official_package_source_unavailable") {
		t.Fatal(p)
	}
	if _, err = os.Stat(filepath.Join(f.dir, "plugin-host", "plugins.sqlite")); !os.IsNotExist(err) {
		t.Fatalf("preview created store: %v", err)
	}
}

func TestPluginMigrationApplyRouteInstallsDisabledAndRejectsStalePlan(t *testing.T) {
	unauth := setup(t)
	if code, _, _ := unauth.request("GET", "/api/plugin-migrations/preview", nil, false, false); code != 401 {
		t.Fatal("preview auth bypass", code)
	}
	useOfficialMigrationCatalog(t)
	f := setup(t)
	f.login()
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.TTSEnabled, legacySpeechFixture.Endpoint, legacySpeechFixture.Key, legacySpeechFixture.TTSModel, legacySpeechFixture.Voice = true, "https://dashscope.aliyuncs.com", "synthetic-private", "speech-model", "voice-a"
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	p, err := f.manager.pluginMigrationPreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	step := previewStep(t, p, "speech")
	if step.Status != "ready_for_administrator_review" {
		t.Fatal(step)
	}
	body := map[string]any{"account": "test", "plugin": "speech", "apply_source_hash": step.ApplySourceHash, "prepared_package_digest": step.Package.ExpectedDigest, "permissions": step.Permissions, "confirm": true}
	code, raw, _ := f.request("POST", "/api/plugin-migrations/apply", body, true, true)
	if code != 201 || strings.Contains(string(raw), "synthetic-private") {
		t.Fatalf("apply %d %s", code, raw)
	}
	s, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	installed, err := s.Installed(context.Background(), "speech")
	if err != nil || installed.Enabled {
		t.Fatalf("unsafe installed %+v %v", installed, err)
	}
	body["apply_source_hash"] = "stale"
	code, _, _ = f.request("POST", "/api/plugin-migrations/apply", body, true, true)
	if code != 409 {
		t.Fatal("stale plan accepted", code)
	}
	code, _, _ = f.request("POST", "/api/plugin-migrations/apply", body, false, false)
	if code != 403 {
		t.Fatal("csrf/origin bypass", code)
	}
}
