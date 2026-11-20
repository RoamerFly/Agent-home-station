package station

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

func TestLiveStationRegisteredMCPThroughChat(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("AHS_LOCAL_EXTERNAL_MCP_TEST") != "1" {
		t.Skip("set AHS_LOCAL_EXTERNAL_MCP_TEST=1 for installed Windows MCP acceptance")
	}
	options := localProjectToolWindowsOptionsFor(t, localExternalPackage)
	base := t.TempDir()
	f := newLocalHomeFixture(t, filepath.Join(base, "host"), filepath.Join(base, "workspace"))
	f.handler.chat.projectTools.options = options
	f.login()
	if code, _ := f.request("POST", "/api/local/home", map[string]string{"action": "initialize"}, true); code != http.StatusOK {
		t.Fatal("isolated station could not initialize")
	}
	const route = "/api/local/project-tools/" + localExternalPackage
	if code, _ := f.request("PUT", route, map[string]any{"action": "install", "approve_artifacts": true}, true); code != http.StatusOK {
		t.Fatalf("external package install: %d", code)
	}
	if code, _ := f.request("PUT", route, map[string]any{"action": "enable", "revision": localProjectToolRevision(t, localProjectToolByID(t, f, localExternalPackage))}, true); code != http.StatusOK {
		t.Fatalf("external package enable: %d", code)
	}
	host := f.handler.chat.projectTools.host
	prepared, err := host.store.Prepare(t.Context(), filepath.Join("..", "..", "templates", "plugins", "python-stdio"))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := host.store.Install(t.Context(), prepared.Token, prepared.Digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.store.SetEnabled(t.Context(), installed.Package.ID, installed.Revision, true); err != nil {
		t.Fatal(err)
	}
	const secret = "synthetic-owner-mcp-header"
	var calls, pages atomic.Int32
	started, cancelled := make(chan struct{}), make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("MCP host omitted the registered authorization")
			http.Error(w, "invalid authorization", 401)
			return
		}
		var request struct {
			ID     json.RawMessage
			Method string
			Params struct {
				Name, Cursor string
				Arguments    struct{ Echo string }
			}
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if len(request.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var response any
		switch request.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "test-mcp-session")
			response = map[string]any{"protocolVersion": "2025-06-18"}
		case "tools/list":
			pages.Add(1)
			if r.Header.Get("Mcp-Session-Id") != "test-mcp-session" {
				t.Error("MCP directory lost its session")
			}
			if request.Params.Cursor == "" {
				response = json.RawMessage(`{"tools":[{"name":"allowed","description":"public opt-in fixture","inputSchema":{"type":"object"}},{"name":"prohibited","inputSchema":{"type":"object"}}],"nextCursor":"provider-cursor-one"}`)
			} else if request.Params.Cursor == "provider-cursor-one" {
				response = json.RawMessage(`{"tools":[{"name":"second","inputSchema":{"type":"object"}}]}`)
			}
		case "tools/call":
			calls.Add(1)
			if request.Params.Name == "allowed" && request.Params.Arguments.Echo == "revoke" {
				close(started)
				<-r.Context().Done()
				close(cancelled)
				return
			}
			if request.Params.Name != "allowed" || request.Params.Arguments.Echo != "station-fixture" {
				t.Error("remote MCP sent an unauthorized or changed invocation")
			}
			response = map[string]any{"content": []map[string]string{{"type": "text", "text": "remote-verified " + secret}}}
		}
		if response == nil {
			http.Error(w, "fixture method or parameters invalid", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": response})
	}))
	defer remote.Close()
	public := os.Getenv("AHS_LOCAL_EXTERNAL_MCP_PUBLIC") == "1"
	servers := []mcptools.ServerConfig{
		{ID: "remote", Transport: "http", URL: remote.URL, Enabled: true, Tools: []string{"allowed", "second"}, Headers: map[string]string{"Authorization": "Bearer " + secret}},
		{ID: "local", Transport: "stdio", PluginID: installed.Package.ID, Enabled: true, Tools: []string{"echo"}},
	}
	if public {
		servers = append(servers, mcptools.ServerConfig{ID: "aws_public", Transport: "http", URL: "https://knowledge-mcp.global.api.aws", Enabled: true, Tools: []string{"aws___read_documentation"}})
	}
	config, _ := json.Marshal(localExternalRegistrationData{MCPServers: servers})
	registration := stationToolRegistrationPath(f.handler.home.dataDir, localExternalRegistration)
	if os.MkdirAll(filepath.Dir(registration), 0700) != nil || os.WriteFile(registration, config, 0600) != nil {
		t.Fatal("could not write the isolated MCP registration")
	}
	round := 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, system string, turns []map[string]string) (string, error) {
			round++
			history := localModelHistory(turns)
			if strings.Contains(history+system, secret) || strings.Contains(history+system, remote.URL) || strings.Contains(history+system, "provider-cursor-one") {
				t.Error("MCP connection credentials, endpoint or raw cursor reached the model")
			}
			last := turns[len(turns)-1]["content"]
			switch round {
			case 1:
				if !strings.Contains(system, localExternalMCPList) {
					t.Error("registered MCP tools not discoverable")
					return `{"action":"reply","messages":["工具不可用。"]}`, nil
				}
				return localToolCall(localExternalMCPList, map[string]any{"server_id": "remote"}), nil
			case 2:
				var packet struct{ Result string }
				var page struct {
					Next string `json:"next_cursor"`
				}
				if json.Unmarshal([]byte(last), &packet) != nil || json.Unmarshal([]byte(packet.Result), &page) != nil ||
					!strings.Contains(last, "remote__allowed") || strings.Contains(last, "prohibited") || page.Next == "" {
					t.Errorf("MCP first page or allowlist was not applied (expected cursor and one tool)")
					return `{"action":"reply","messages":["目录读取失败。"]}`, nil
				}
				return localToolCall(localExternalMCPList, map[string]any{"server_id": "remote", "cursor": page.Next}), nil
			case 3:
				if !strings.Contains(last, "remote__second") {
					t.Error("MCP next page missing")
				}
				return localToolCall(localExternalMCPCall, map[string]any{"server_id": "remote", "tool": "remote__prohibited", "arguments": map[string]string{}}), nil
			case 4:
				if calls.Load() != 0 {
					t.Error("unapproved remote tool was invoked")
				}
				return localToolCall(localExternalMCPCall, map[string]any{"server_id": "remote", "tool": "remote__allowed", "arguments": map[string]string{"echo": "station-fixture"}}), nil
			case 5:
				if !strings.Contains(last, "remote-verified") || !strings.Contains(last, "[已隐藏]") {
					t.Error("MCP verified output did not reach the model")
				}
				return localToolCall(localExternalMCPCall, map[string]any{"server_id": "remote", "tool": "remote__allowed", "arguments": map[string]string{"echo": "station-fixture"}, "text_offset": 7}), nil
			case 6:
				if calls.Load() != 1 || !strings.Contains(last, `\"reused\":true`) {
					t.Error("MCP continuation re-executed the call")
				}
				return localToolCall(localExternalMCPList, map[string]any{"server_id": "local"}), nil
			case 7:
				if !strings.Contains(last, "local__echo") || strings.Contains(last, "job_start") {
					t.Error("registered stdio tool list or allowlist missing")
				}
				return localToolCall(localExternalMCPCall, map[string]any{"server_id": "local", "tool": "local__echo", "arguments": map[string]string{"text": "windows-isolated-mcp-echo"}}), nil
			case 8:
				if !strings.Contains(last, "windows-isolated-mcp-echo") {
					t.Error("real stdio response missing")
				}
				if public {
					return localToolCall(localExternalMCPList, map[string]any{"server_id": "aws_public"}), nil
				}
			case 9:
				if !strings.Contains(last, "aws_public__aws___read_documentation") {
					t.Error("public MCP catalog missing")
				}
				args := map[string]any{"requests": []map[string]any{{"url": "https://docs.aws.amazon.com/AmazonS3/latest/userguide/Welcome.html", "max_length": 1000}}}
				return localToolCall(localExternalMCPCall, map[string]any{"server_id": "aws_public", "tool": "aws_public__aws___read_documentation", "arguments": args}), nil
			case 10:
				if !strings.Contains(strings.ToLower(last), "amazon s3") {
					t.Error("public AWS documentation did not reach chat")
				}
			}
			return `{"action":"reply","messages":["已核对工具目录和实际结果。"]}`, nil
		}), nil
	}
	code, view := localChatSend(f, "registered-mcp-tools", "读取授权的 MCP 测试结果。")
	if code != http.StatusOK || view["status"] != "completed" || calls.Load() != 1 || pages.Load() != 2 || round < 8 || public && round < 10 {
		t.Fatalf("MCP chat did not complete: code=%d state=%v pages=%d calls=%d model_steps=%d", code, view["status"], pages.Load(), calls.Load(), round)
	}
	modelStep := 0
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, _ string, _ []map[string]string) (string, error) {
			modelStep++
			if modelStep == 1 {
				args := map[string]any{"server_id": "remote", "tool": "remote__allowed", "arguments": map[string]string{"echo": "revoke"}}
				return localToolCall(localExternalMCPCall, args), nil
			}
			return `{"action":"reply","messages":["已停止等待本机连接。"]}`, nil
		}), nil
	}
	removed := make(chan error, 1)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-started:
			removed <- os.Remove(registration)
		case <-stop:
		}
	}()
	_, _ = localChatSend(f, "registered-mcp-inflight-revoke", "验证撤销正在等待的 MCP 请求。")
	select {
	case err := <-removed:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("MCP call did not reach the revocation barrier")
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("revoking the registration did not cancel the inflight HTTP request")
	}
	if calls.Load() != 2 {
		t.Fatal("revocation repeated the original remote call")
	}
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, system string, _ []map[string]string) (string, error) {
			if strings.Contains(system, localExternalMCPCall) {
				t.Error("revoked MCP registration still shown")
			}
			return `{"action":"reply","messages":["已核对登记。"]}`, nil
		}), nil
	}
	if code, _ := localChatSend(f, "registered-mcp-revoked", "核对已撤销连接。"); code != http.StatusOK {
		t.Fatalf("station chat after revocation: %d", code)
	}
	t.Logf("remote_mcp_pages=%d remote_calls=%d allowlist=true registered_stdio_lpac=true secret_masked=true inflight_revocation_cancelled=true revoked_undiscoverable=true public_aws=%v", pages.Load(), calls.Load(), public)
}
