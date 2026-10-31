package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/plugins"
)

func TestPluginLifecycleAndGeneratedVoice(t *testing.T) {
	f := setup(t)
	native := &fakeTools{fakeNative: f.native}
	f.manager.native = native
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer private-plugin-key" {
			t.Error("missing private credential")
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Write([]byte("RIFF mock audio"))
	}))
	defer upstream.Close()
	manifest := plugins.Manifest{ID: "voice", Name: "测试语音", Version: "1", Tools: []plugins.APITool{{Name: "speak", Description: "合成语音", URL: upstream.URL, InputSchema: json.RawMessage(`{"type":"object"}`), Headers: map[string]string{"Authorization": "Bearer {{secret.KEY}}"}, MediaKind: "voice"}}}
	if code, _, _ := f.request("GET", "/api/plugins", nil, false, false); code != 401 {
		t.Fatal("unprotected plugin credentials")
	}
	f.login()
	input := map[string]any{"manifest": manifest, "enabled": true, "secrets": map[string]string{"KEY": "private-plugin-key"}}
	if code, _, _ := f.request("POST", "/api/plugins/install", input, true, false); code != 403 {
		t.Fatal("install bypassed CSRF")
	}
	if code, _, _ := f.request("POST", "/api/plugins/install", input, true, true); code != 200 {
		t.Fatal("install", code)
	}
	entry, _ := f.manager.plugins.get("voice")
	if entry.Enabled {
		t.Fatal("installation unexpectedly enabled remote calls")
	}
	if code, body, _ := f.request("GET", "/api/plugins", nil, false, false); code != 200 || bytes.Contains(body, []byte("private-plugin-key")) {
		t.Fatal("plugin key returned")
	}
	if code, _, _ := f.request("POST", "/api/plugins/voice", map[string]any{"enabled": true}, true, true); code != 200 {
		t.Fatal("enable", code)
	}
	reloaded, err := newPluginLedger(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.plugins = reloaded
	access, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	result, err := access.Call(context.Background(), "voice__speak", json.RawMessage(`{"text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Media []companion.GeneratedMedia `json:"media"`
	}
	if json.Unmarshal([]byte(result), &output) != nil || len(output.Media) != 1 {
		t.Fatal("no generated voice", result)
	}
	if item, ok := access.Media(output.Media[0].ID); !ok || item.Kind != "voice" {
		t.Fatal("voice ID unavailable")
	}
	if native.submits.Load() != 0 || calls.Load() != 1 {
		t.Fatal("staging must not send or retry")
	}
	access.ResetMedia()
	if _, ok := access.Media(output.Media[0].ID); ok {
		t.Fatal("previous conversation media leaked")
	}
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.SendVoice = false
	f.manager.mu.Unlock()
	tools, err := access.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Name == "voice__speak" {
			t.Fatal("disabled paid capability advertised")
		}
	}
	if _, err = access.Call(context.Background(), "voice__speak", json.RawMessage(`{}`)); err == nil || calls.Load() != 1 {
		t.Fatal("disabled voice called provider")
	}
	if code, _, _ := f.request("POST", "/api/plugins/voice/remove", map[string]any{}, true, true); code != 200 {
		t.Fatal("remove", code)
	}
	if len(f.manager.plugins.installed()) != 0 {
		t.Fatal("removed plugin remained active")
	}
	if code, body, _ := f.request("GET", "/api/plugins", nil, true, false); code != 200 || !bytes.Contains(body, []byte(`"removed":[`)) || bytes.Contains(body, []byte("private-plugin-key")) {
		t.Fatal("removed list unavailable or leaked secrets")
	}
	if code, _, _ := f.request("POST", "/api/plugins/voice", map[string]bool{"enabled": false}, true, true); code != 200 {
		t.Fatal("restore", code)
	}
	entry, _ = f.manager.plugins.get("voice")
	if entry.Removed || entry.Enabled || entry.Secrets["KEY"] != "private-plugin-key" {
		t.Fatal("restore lost credentials or enabled plugin")
	}
}

func TestMCPConfigCannotConflictWithEnabledPlugin(t *testing.T) {
	f := setup(t)
	f.login()
	manifest := plugins.Manifest{ID: "duplicate", Name: "MCP", MCP: &mcptools.ServerConfig{Transport: "http", URL: "https://example.org/mcp"}}
	if code, _, _ := f.request("POST", "/api/plugins/install", map[string]any{"manifest": manifest}, true, true); code != 200 {
		t.Fatal(code)
	}
	if code, _, _ := f.request("POST", "/api/plugins/duplicate", map[string]bool{"enabled": true}, true, true); code != 200 {
		t.Fatal(code)
	}
	config := mcpDefaults()
	config.Servers = []mcptools.ServerConfig{{ID: "duplicate", Name: "Conflict", Transport: "http", URL: "https://example.org/mcp", Enabled: true}}
	if code, _, _ := f.request("POST", "/api/mcp", config, true, true); code != 400 {
		t.Fatal("conflicting MCP accepted", code)
	}
}

func TestInlineMCPAudioStagedAndPermissionCheckedBeforeCalling(t *testing.T) {
	f := setup(t)
	f.manager.native = &fakeTools{fakeNative: f.native}
	var generated atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		if request.Method == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		result := map[string]any{"protocolVersion": "2025-06-18"}
		if request.Method == "tools/call" {
			generated.Add(1)
			result = map[string]any{"content": []map[string]string{{"type": "audio", "mimeType": "audio/wav", "data": "UklGRg=="}}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer remote.Close()
	err := f.manager.plugins.save(pluginEntry{Installed: plugins.Installed{Enabled: true, Manifest: plugins.Manifest{ID: "mcpvoice", Name: "MCP voice", MCP: &mcptools.ServerConfig{Transport: "http", URL: remote.URL}, ToolMediaKinds: map[string]string{"sing": "voice"}}}})
	if err != nil {
		t.Fatal(err)
	}
	access, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	output, err := access.Call(context.Background(), "mcpvoice__sing", json.RawMessage(`{}`))
	if err != nil || !bytes.Contains([]byte(output), []byte("source-one")) {
		t.Fatal("MCP audio not staged", output, err)
	}
	if bytes.Contains([]byte(output), []byte("UklGRg")) {
		t.Fatal("raw binary exposed to language model")
	}
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.SendVoice = false
	f.manager.mu.Unlock()
	if _, err := access.Call(context.Background(), "mcpvoice__sing", json.RawMessage(`{}`)); err == nil || generated.Load() != 1 {
		t.Fatal("disabled MCP voice still billed")
	}
}
