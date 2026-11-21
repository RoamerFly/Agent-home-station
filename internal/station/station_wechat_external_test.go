package station

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/plugins"
)

func TestWechatExternalCatalogUsesCurrentWorkstationRegistration(t *testing.T) {
	if os.Getenv("AHS_LOCAL_PROJECT_TOOL_RUNTIME_ROOT") == "" {
		t.Skip("Windows project-tool runtime is required for catalog integration")
	}
	f := localChatFixture(t)
	f.handler.chat.projectTools.options = localProjectToolWindowsOptionsFor(t, localExternalPackage)
	if code, out := f.request("GET", "/api/local/project-tools", nil, true); code != http.StatusOK {
		t.Fatal(code, out)
	}
	route := "/api/local/project-tools/" + localExternalPackage
	if code, out := f.request("PUT", route, map[string]any{"action": "install", "approve_artifacts": true}, true); code != http.StatusOK {
		t.Fatal(code, out)
	}
	installed := localProjectToolByID(t, f, localExternalPackage)
	if code, out := f.request("PUT", route, map[string]any{"action": "enable", "revision": localProjectToolRevision(t, installed)}, true); code != http.StatusOK {
		t.Fatal(code, out)
	}
	const secret = "synthetic-private-key"
	registration := map[string]any{"connections": []plugins.Installed{{Manifest: plugins.Manifest{ID: "synthetic", Name: "Synthetic", Version: "1.0.0",
		Tools: []plugins.APITool{{Name: "lookup", Description: "Read a synthetic record", URL: "https://fixture.example.test/read", Method: "GET",
			Headers:     map[string]string{"Authorization": "Bearer {{secret.api_key}}"},
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}}},
		Enabled: true, Secrets: map[string]string{"api_key": secret}}}}
	path := stationToolRegistrationPath(f.handler.home.dataDir, localExternalRegistration)
	raw, _ := json.Marshal(registration)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "native-token")
	if err := os.WriteFile(key, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := linuxnative.New(linuxnative.Config{URL: "http://127.0.0.1:1", TokenFile: key, Account: "synthetic-account"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	in := companion.Input{Channel: "wechat", Account: native.Account(), Thread: "test@chatroom", Sender: "owner", TraceID: "synthetic-catalog",
		MessageTime: now.Format(time.RFC3339Nano), Mode: "direct", Directed: true}
	adapter := &stationAttachmentTools{native: native, projects: f.handler.chat.projectTools,
		grants: map[string]time.Time{in.Thread: now.Add(-time.Minute)}, blocked: func() bool { return false },
		projectOwnerAllowed: func(current companion.Input) bool { return current.Sender == in.Sender }}
	adapter.SetTrace(in)
	listed, err := adapter.List(t.Context())
	if err != nil || !hasTool(listed, localExternalHTTPList) || hasTool(listed, localExternalHTTPCall) {
		t.Fatal("application catalog did not distinguish discovery from execution", err)
	}
	result, err := adapter.Call(t.Context(), localExternalHTTPList, json.RawMessage(`{"limit":1}`))
	if err != nil || !strings.Contains(result, "synthetic__lookup") || strings.Contains(result, secret) || strings.Contains(result, "fixture.example.test") {
		t.Fatal("workstation catalog leaked private connection settings", result, err)
	}
	localStore, localInput, err := f.handler.chat.conversation()
	if err != nil {
		t.Fatal(err)
	}
	public := f.handler.chat.tools(localStore, localInput)
	public.publicUseOnly = true
	public.run = companion.NewGuardedToolRun(func(context.Context) error { return nil })
	listed, err = public.List(t.Context())
	if err != nil || hasTool(listed, localExternalHTTPList) || hasTool(listed, localExternalHTTPCall) {
		t.Fatal("source-bound HTTP tools leaked into the application", err)
	}
	if _, err := public.Call(t.Context(), localExternalHTTPCall, json.RawMessage(`{}`)); err == nil {
		t.Fatal("application bypassed its external tool adapter")
	}
	installed = localProjectToolByID(t, f, localExternalPackage)
	if code, out := f.request("PUT", route, map[string]any{"action": "disable", "revision": localProjectToolRevision(t, installed)}, true); code != http.StatusOK {
		t.Fatal(code, out)
	}
	listed, err = adapter.List(t.Context())
	if err != nil || hasTool(listed, localExternalHTTPList) {
		t.Fatal("disabled workstation catalog remained visible", err)
	}
	if _, err := adapter.Call(t.Context(), localExternalHTTPList, json.RawMessage(`{}`)); err == nil {
		t.Fatal("disabled workstation catalog remained callable")
	}
}
