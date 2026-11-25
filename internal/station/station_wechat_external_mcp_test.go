package station

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

func TestWechatExternalMCPDirectoryAndSubmitOnce(t *testing.T) {
	if os.Getenv("AHS_LOCAL_PROJECT_TOOL_RUNTIME_ROOT") == "" {
		t.Skip("Windows project-tool runtime is required")
	}
	f := localChatFixture(t)
	f.handler.chat.projectTools.options = localProjectToolWindowsOptionsFor(t, localExternalPackage)
	if code, out := f.request("GET", "/api/local/project-tools", nil, true); code != http.StatusOK {
		t.Fatal(code, out)
	}
	route := "/api/local/project-tools/" + localExternalPackage
	for _, action := range []string{"install", "enable"} {
		input := map[string]any{"action": action}
		if action == "install" {
			input["approve_artifacts"] = true
		} else {
			input["revision"] = localProjectToolRevision(t, localProjectToolByID(t, f, localExternalPackage))
		}
		if code, out := f.request("PUT", route, input, true); code != http.StatusOK {
			t.Fatal(code, out)
		}
	}
	const secret = "synthetic-mcp-private-secret"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("MCP credential missing")
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
				Name   string `json:"name"`
			} `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if len(request.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "synthetic-session")
			result = map[string]string{"protocolVersion": "2025-06-18"}
		case "tools/list":
			if request.Params.Cursor != "" && r.Header.Get("Mcp-Session-Id") != "synthetic-session" {
				t.Error("MCP page lost its session")
			}
			if request.Params.Cursor == "" {
				result = json.RawMessage(`{"tools":[{"name":"allowed","description":"public tool","inputSchema":{"type":"object"}},{"name":"images","inputSchema":{"type":"object"}},{"name":"denied"}],"nextCursor":"private-provider-cursor"}`)
			} else if request.Params.Cursor == "private-provider-cursor" {
				result = json.RawMessage(`{"tools":[{"name":"second","inputSchema":{"type":"object"}}]}`)
			}
		case "tools/call":
			calls.Add(1)
			if request.Params.Name != "allowed" && request.Params.Name != "images" {
				t.Error("unapproved MCP tool invoked")
			}
			if request.Params.Name == "images" {
				result = map[string]any{"content": []map[string]string{{"type": "image", "data": base64.StdEncoding.EncodeToString([]byte("synthetic-image-one")), "mimeType": "image/png"},
					{"type": "image", "data": base64.StdEncoding.EncodeToString([]byte("synthetic-image-two")), "mimeType": "image/png"}}}
			} else {
				result = map[string]any{"content": []map[string]string{{"type": "text", "text": "verified " + secret}}}
			}
		}
		if result == nil {
			http.Error(w, "unexpected MCP request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer server.Close()
	registration, _ := json.Marshal(localExternalRegistrationData{MCPServers: []mcptools.ServerConfig{{ID: "remote", Transport: "http", URL: server.URL,
		Enabled: true, Tools: []string{"allowed", "images", "second"}, Headers: map[string]string{"Authorization": "Bearer " + secret}}}})
	path := stationToolRegistrationPath(f.handler.home.dataDir, localExternalRegistration)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, registration, 0600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "native-token")
	if err := os.WriteFile(key, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := linuxnative.New(linuxnative.Config{URL: server.URL, TokenFile: key, Account: "synthetic-account"})
	if err != nil {
		t.Fatal(err)
	}
	memory, err := companion.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	now := time.Now().UTC()
	in := companion.Input{Channel: "wechat", Account: native.Account(), Thread: "synthetic-thread", Sender: "owner", TraceID: "synthetic-mcp", Text: "run",
		MessageTime: now.Format(time.RFC3339Nano), Mode: "direct", Directed: true}
	in.Source = &companion.SourceRef{InstanceID: memory.SourceInstanceID(), MessageID: in.TraceID, Account: in.Account, Thread: in.Thread,
		Sender: in.Sender, OccurredAt: in.MessageTime}
	config := companion.Defaults()
	config.Permissions.Remember = true
	if err := memory.Observe(in, config, now); err != nil {
		t.Fatal(err)
	}
	in.Source, _ = memory.SourceForInput(in)
	grant := now.Add(-time.Minute)
	adapter := &stationAttachmentTools{native: native, store: memory, dir: t.TempDir(), projects: f.handler.chat.projectTools,
		grants: map[string]time.Time{in.Thread: grant}, replies: map[string]time.Time{in.Thread: grant}, blocked: func() bool { return false }, externalMu: new(sync.Mutex),
		projectOwnerAllowed: func(current companion.Input) bool { return current.Sender == in.Sender }}
	adapter.SetTrace(in)
	listed, err := adapter.List(context.Background())
	if err != nil || !hasTool(listed, localExternalMCPList) || !hasTool(listed, localExternalMCPCall) {
		t.Fatal("MCP tools missing", err)
	}
	first, err := adapter.Call(t.Context(), localExternalMCPList, json.RawMessage(`{"server_id":"remote"}`))
	if err != nil || !strings.Contains(first, "remote__allowed") || strings.Contains(first, "denied") || strings.Contains(first, "private-provider-cursor") || strings.Contains(first, secret) {
		t.Fatal("first page invalid", first, err)
	}
	var page struct {
		Next string `json:"next_cursor"`
	}
	if json.Unmarshal([]byte(first), &page) != nil || page.Next == "" {
		t.Fatal("missing opaque cursor", first)
	}
	secondArgs, _ := json.Marshal(map[string]string{"server_id": "remote", "cursor": page.Next})
	second, err := adapter.Call(t.Context(), localExternalMCPList, secondArgs)
	if err != nil || !strings.Contains(second, "remote__second") {
		t.Fatal("second page invalid", second, err)
	}
	call := json.RawMessage(`{"server_id":"remote","tool":"remote__allowed","arguments":{},"text_limit":8}`)
	firstCall, err := adapter.Call(t.Context(), localExternalMCPCall, call)
	if err != nil || calls.Load() != 1 || !strings.Contains(firstCall, "verified") || strings.Contains(firstCall, secret) {
		t.Fatal("MCP call invalid", firstCall, err)
	}
	secondCall, err := adapter.Call(t.Context(), localExternalMCPCall, json.RawMessage(`{"server_id":"remote","tool":"remote__allowed","arguments":{},"text_offset":8}`))
	if err != nil || calls.Load() != 1 || !strings.Contains(secondCall, `"reused":true`) {
		t.Fatal("MCP call replayed", secondCall, err)
	}
	images, err := adapter.Call(t.Context(), localExternalMCPCall, json.RawMessage(`{"server_id":"remote","tool":"remote__images","arguments":{}}`))
	if err != nil || calls.Load() != 2 || !strings.Contains(images, "external-mcp-") || strings.Count(images, `"asset_id"`) != 2 || !strings.Contains(images, `"state":"completed"`) {
		t.Fatal("multiple MCP media not preserved", images, err)
	}
}
