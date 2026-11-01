package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/mcptools"
)

func TestBuiltinSchemasRejectWrongShapesBeforeAnyAction(t *testing.T) {
	f, p := trustedAgent(t)
	c := f.manager.agent.config()
	c.Host.Mode = "full"
	c.CrossConversation = true
	c.AccountActions = []string{"rename"}
	c.Host.Windows = &agenthost.WindowsConfig{Pythonw: filepath.Join(c.Host.Roots[0], "pythonw.exe"), Helper: filepath.Join(c.Host.Roots[0], "helper.pyw"), Spool: filepath.Join(c.Host.Roots[0], "private")}
	if e := f.manager.agent.save(c); e != nil {
		t.Fatal(e)
	}
	f.manager.speech.Key = "synthetic-local-fixture"
	f.manager.speech.TTSEnabled = true
	f.manager.speech.MusicEnabled = true
	f.manager.speech.YuEEnabled = true
	f.manager.speech.YuEEndpoint = "http://127.0.0.1:1"
	f.manager.speech.YuEToken = "synthetic-local-token"
	installLegacyLocalMusicPlugin(t, f.manager)
	mobile := f.manager.mobile.Settings()
	mobile.Enabled = true
	mobile.Bridge.Command = "synthetic-not-executed-device"
	if e := f.manager.mobile.Save(mobile); e != nil {
		t.Fatal(e)
	}
	tools := append(p.localTools(), mcptools.BuiltinTools()...)
	if len(tools) < 39 {
		t.Fatal("incomplete built-in schema coverage", len(tools))
	}
	t.Logf("validating %d tool schemas without model, network or send side effects", len(tools))
	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			for _, raw := range []string{`null`, `[]`, `"text"`, `{"a":1} {}`, `{"a":1,"a":2}`} {
				if _, e := p.normalizeToolArgs(tool.Name, json.RawMessage(raw)); e == nil {
					t.Fatal("invalid shape accepted", raw)
				}
			}
			var schema inputSchema
			if json.Unmarshal(tool.InputSchema, &schema) != nil {
				t.Fatal("invalid schema")
			}
			if len(schema.Required) > 0 {
				if _, e := p.normalizeToolArgs(tool.Name, json.RawMessage(`{}`)); e == nil {
					t.Fatal("required fields missing but accepted")
				}
			}
		})
	}
}
func TestToolArgumentCompatibilityNeverChangesMeaning(t *testing.T) {
	f, p := trustedAgent(t)
	c := f.manager.agent.config()
	c.Host.Mode = "full"
	f.manager.agent.save(c)
	args, e := p.normalizeToolArgs("server_process", json.RawMessage(`{"action":"start","path":"/test","command":"echo hi","requestKey":"k","timeoutSeconds":"1e3"}`))
	if e != nil || !strings.Contains(string(args), `"timeout_seconds":1000`) || !strings.Contains(string(args), `"request_key":"k"`) {
		t.Fatal(string(args), e)
	}
	for _, raw := range []string{`{"action":"start","timeout_seconds":"1.5"}`, `{"action":"start","requestKey":"a","request_key":"b"}`, `{"action":"start","timeout_seconds":true}`, `{"action":"not-an-action"}`, `{"action":"start","unexpected_secret":"never-print-this"}`} {
		_, e := p.normalizeToolArgs("server_process", json.RawMessage(raw))
		if e == nil || strings.Contains(mcptools.FailureDescription(e), "never-print-this") {
			t.Fatal(raw, e)
		}
	}
	_, e = p.Call(t.Context(), "server_write", json.RawMessage(`{"path":"secret","content":null}`))
	if e == nil || !strings.Contains(mcptools.FailureDescription(e), "校验阶段未执行工具") {
		t.Fatal("invalid write reached execution", e)
	}
}
func TestExternalMCPArgumentTypesAndNamesAreNotRewritten(t *testing.T) {
	_, p := trustedAgent(t)
	raw := json.RawMessage(`{"camelField":"true","seed":"9223372036854775807"}`)
	result, e := p.normalizeToolArgs("external__tool", raw)
	if e != nil || string(result) != string(raw) {
		t.Fatal(string(result), e)
	}
}

func TestCapabilityCatalogKeepsLocalToolsWhenExternalServerFails(t *testing.T) {
	f, _ := trustedAgent(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	f.manager.mcp.Servers = []mcptools.ServerConfig{{ID: "broken", Transport: "http", URL: server.URL, Enabled: true, TimeoutSeconds: 1}}
	f.login()
	code, b, _ := f.request("GET", "/api/agent/capabilities?owner=wxid_owner", nil, false, false)
	if code != 200 || !strings.Contains(string(b), `"partial":true`) || !strings.Contains(string(b), `"server_read"`) {
		t.Fatal(code, string(b))
	}
}

func TestRejectedProcessCanRetryAfterEnvironmentIsRepaired(t *testing.T) {
	f, p := trustedAgent(t)
	c := f.manager.agent.config()
	c.Host.Mode = "full"
	if e := f.manager.agent.save(c); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(c.Host.Roots[0], "missing")
	args, _ := json.Marshal(map[string]any{"action": "start", "path": dir, "command": "echo recovered", "request_key": "rejected-no-effect"})
	if _, e := p.Call(t.Context(), "server_process", args); e == nil || !strings.Contains(mcptools.FailureDescription(e), "程序未启动") {
		t.Fatal(e)
	}
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	result, e := p.Call(t.Context(), "server_process", args)
	if e != nil || !strings.Contains(result, `"id":"process-`) {
		t.Fatal(result, e)
	}
	f.manager.processes.Close()
}
