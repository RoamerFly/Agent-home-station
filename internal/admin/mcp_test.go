package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginruntime"
)

func TestMCPConfigurationSecretsAndAbilitySwitches(t *testing.T) {
	f := setup(t)
	if code, _, _ := f.request("GET", "/api/mcp", nil, false, false); code != 401 {
		t.Fatal("unauthenticated MCP configuration")
	}
	f.login()
	settings := mcpDefaults()
	settings.Servers = []mcptools.ServerConfig{{ID: "example", Name: "Example", Transport: "http", URL: "https://example.org/mcp", Enabled: true, Env: map[string]string{"TOKEN": "private-mcp-secret"}, Headers: map[string]string{"Authorization": "Bearer private-mcp-header"}}}
	if code, _, _ := f.request("POST", "/api/mcp", settings, true, false); code != 403 {
		t.Fatal("MCP save omitted CSRF")
	}
	code, data, _ := f.request("POST", "/api/mcp", settings, true, true)
	if code != 200 || bytes.Contains(data, []byte("private-mcp")) {
		t.Fatal("MCP credentials leaked", code)
	}
	var view mcpSettings
	if err := json.Unmarshal(data, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Servers[0].Env) != 0 || len(view.Servers[0].Headers) != 0 {
		t.Fatal("MCP secret return")
	}
	if code, _, _ := f.request("POST", "/api/mcp", view, true, true); code != 200 {
		t.Fatal("blank MCP secret preservation failed")
	}
	loaded, err := loadMCP(f.dir)
	if err != nil || loaded.Servers[0].Env["TOKEN"] != "private-mcp-secret" {
		t.Fatal("credential lost", err)
	}
	view.Servers[0].URL = "https://elsewhere.example/mcp"
	if code, _, _ := f.request("POST", "/api/mcp", view, true, true); code != 400 {
		t.Fatal("saved secrets moved to different endpoint")
	}
	settings = mcpDefaults()
	settings.WebSearchEnabled = false
	if code, _, _ := f.request("POST", "/api/mcp", settings, true, true); code != 200 {
		t.Fatal("switch save")
	}
	access, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := access.List(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "web_media_download" {
		t.Fatal("search capability not removed")
	}
	if _, err = access.Call(context.Background(), "web_search", json.RawMessage(`{"query":"x"}`)); err == nil {
		t.Fatal("disabled search still callable")
	}
	settings.Enabled = false
	if code, _, _ := f.request("POST", "/api/mcp", settings, true, true); code != 200 {
		t.Fatal("disableall")
	}
	access, _, err = f.manager.toolAccess()
	if err != nil || access == nil {
		t.Fatal("local Agent dispatcher unavailable", err)
	}
	tools, err = access.List(context.Background())
	if err != nil || len(tools) != 0 {
		t.Fatal("disabled MCP tools offered", tools, err)
	}
	if _, err = access.Call(context.Background(), "web_fetch", json.RawMessage(`{"url":"https://example.org/"}`)); err == nil {
		t.Fatal("disabled MCP still callable")
	}
}

func TestLegacyOfficialSearchRequiresPluginMigrationWithoutNetwork(t *testing.T) {
	f := setup(t)
	f.login()
	f.manager.mu.Lock()
	f.manager.config.Provider = "deepseek"
	f.manager.config.BaseURL = "https://api.deepseek.com"
	f.manager.config.APIKey = "synthetic-compatibility-key-123"
	f.manager.mu.Unlock()

	access, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	_, err = access.Call(context.Background(), "web_search", json.RawMessage(`{"query":"Traveling Light"}`))
	var web *mcptools.WebError
	if !errors.As(err, &web) || web.Code != "web_plugin_migration_required" {
		t.Fatalf("legacy paid search error=%v", err)
	}
}

func TestInstalledWebPluginHandlesChineseAudioAlias(t *testing.T) {
	f, access := trustedAgent(t)
	ctx := context.Background()
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(ctx, "../../plugins/web-search")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(ctx, prepared.Token, prepared.Digest, []string{"network"})
	if err != nil {
		t.Fatal(err)
	}
	installed, err = store.SetEnabled(ctx, installed.Package.ID, installed.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	executor := &packageTestExecutor{}
	runtime, err := pluginruntime.NewController(store, executor)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime, f.manager.packageRuntimeReady = runtime, true
	if err = runtime.Start(ctx, installed.Package.ID, installed.Revision); err != nil {
		t.Fatal(err)
	}
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.SendVoice = false
	f.manager.config.Companion.Permissions.SendFile = false
	f.manager.mu.Unlock()
	if access.enabled("web_audio_search") {
		t.Fatal("installed audio alias bypassed attachment permissions")
	}
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.SendVoice = true
	f.manager.mu.Unlock()
	tools, err := access.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var alias string
	for _, tool := range tools {
		if tool.Name == "web_audio_search" {
			alias = tool.Description
		}
	}
	if alias == "" || !strings.Contains(alias, "不会下载、播放或发送") {
		t.Fatalf("audio alias missing or overclaims delivery: %q", alias)
	}
	// The synthetic runtime returns an unverifiable empty result, so the call
	// may fail validation. Its one invocation proves the alias used installed
	// web.search rather than a legacy paid or public-catalog fallback.
	_, _ = access.Call(ctx, "web_audio_search", json.RawMessage(`{"query":"周杰伦 稻香"}`))
	if executor.calls != 1 {
		t.Fatalf("audio alias did not use installed web.search: calls=%d", executor.calls)
	}
	installed, err = store.Installed(ctx, installed.Package.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetEnabled(ctx, installed.Package.ID, installed.Revision, false); err != nil {
		t.Fatal(err)
	}
	_, err = access.Call(ctx, "web_audio_search", json.RawMessage(`{"query":"周杰伦 稻香"}`))
	web := (*mcptools.WebError)(nil)
	if !errors.As(err, &web) || web.Code != "web_plugin_migration_required" {
		t.Fatalf("disabled audio alias fell through: %v", err)
	}
	if executor.calls != 1 {
		t.Fatalf("disabled audio alias reached runtime: calls=%d", executor.calls)
	}
}

func TestPublicMediaHonorsPermissionsBeforeDownloadAndReportsMissingURL(t *testing.T) {
	f, access := trustedAgent(t)
	f.login()
	installPublicWebFixture(t, f)
	_, err := access.Call(context.Background(), "web_fetch", json.RawMessage(`{"query":"deepseek"}`))
	if err == nil || !bytes.Contains([]byte(mcptools.FailureDescription(err)), []byte("url")) {
		t.Fatal("missing URL diagnostic")
	}
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.SendVoice = false
	f.manager.mu.Unlock()
	// Invalid endpoint deliberately: permission must be rejected before DNS/HTTP.
	if _, err = access.Call(context.Background(), "web_media_download", json.RawMessage(`{"kind":"voice","url":"https://must-not-resolve.invalid/a.mp3"}`)); err == nil || err.Error() != "media_permission_denied" {
		t.Fatal("permission check too late", err)
	}
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.SendVideo = false
	f.manager.mu.Unlock()
	if _, err = access.Call(context.Background(), "web_media_download", json.RawMessage(`{"kind":"video","url":"https://www.bilibili.com/video/BV1qP4y187Lf/"}`)); err == nil || err.Error() != "media_permission_denied" {
		t.Fatal("video page read before permission check", err)
	}
	if code, body, _ := f.request("POST", "/api/mcp/test", map[string]any{"name": "web_fetch", "arguments": map[string]string{"query": "deepseek"}}, true, true); code != 502 || !bytes.Contains(body, []byte("工具能力已关闭")) {
		t.Fatal("manual tool bypassed conversation authority", code, string(body))
	}
}
