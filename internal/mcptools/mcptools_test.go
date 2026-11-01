package mcptools

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestHelperMCPProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}
	r := bufio.NewScanner(os.Stdin)
	for r.Scan() {
		var q rpcRequest
		if json.Unmarshal(r.Bytes(), &q) != nil || q.Method == "notifications/initialized" {
			continue
		}
		var result any
		switch q.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": protocolVersion}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "echo", "description": "echoes", "inputSchema": map[string]any{"type": "object"}}, {"name": "hidden"}}}
		case "tools/call":
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": "stdio result"}}}
		default:
			result = map[string]any{}
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result})
		_, _ = os.Stdout.Write(append(b, '\n'))
	}
	os.Exit(0)
}

func TestStdioListAndCallRespectAllowlist(t *testing.T) {
	cfg := ServerConfig{ID: "local", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=TestHelperMCPProcess"}, Env: map[string]string{"GO_WANT_MCP_HELPER": "1"}, Enabled: true, Tools: []string{"echo"}}
	h, err := New([]ServerConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	ts, err := h.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasTool(ts, "web_search") || !hasTool(ts, "local__echo") || hasTool(ts, "local__hidden") {
		t.Fatalf("unexpected tools: %#v", ts)
	}
	out, err := h.Call(context.Background(), "local__echo", json.RawMessage(`{"v":1}`))
	if err != nil || out != "stdio result" {
		t.Fatalf("call = %q, %v", out, err)
	}
}

func TestHTTPMCPInitializeSessionAndSSE(t *testing.T) {
	var mu sync.Mutex
	var methods, sessions, auth []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&q)
		mu.Lock()
		methods = append(methods, q.Method)
		sessions = append(sessions, r.Header.Get("Mcp-Session-Id"))
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		if q.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "test-session")
		}
		if q.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch q.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": protocolVersion}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "ping"}}}
		case "tools/call":
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": "pong"}}}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		b, _ := json.MarshalIndent(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result}, "", "  ")
		for _, line := range strings.Split(string(b), "\n") {
			_, _ = w.Write([]byte("data: " + line + "\n"))
		}
		_, _ = w.Write([]byte("\n"))
	}))
	defer s.Close()
	h, err := New([]ServerConfig{{ID: "remote", Transport: "http", URL: s.URL, Enabled: true, Headers: map[string]string{"Authorization": "Bearer configured-owner-secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	ts, err := h.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !hasTool(ts, "remote__ping") {
		t.Fatal("missing HTTP tool")
	}
	out, err := h.Call(context.Background(), "remote__ping", json.RawMessage(`{}`))
	if err != nil || out != "pong" {
		t.Fatalf("call = %q, %v", out, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 6 {
		t.Fatalf("methods=%v", methods)
	}
	for i := range sessions {
		if methods[i] != "initialize" && sessions[i] != "test-session" {
			t.Fatalf("missing session on %s: %q", methods[i], sessions[i])
		}
		if auth[i] != "Bearer configured-owner-secret" {
			t.Fatalf("configured auth header not sent")
		}
	}
}

func TestPublicFetchBlocksLoopbackAndCredentials(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1/", "http://user:pass@example.com/", "file:///etc/passwd", "https://example.com/#fragment"} {
		if err := validPublicURL(raw); err == nil {
			if strings.Contains(raw, "127.0.0.1") {
				_, _, _, err = publicRead(context.Background(), raw, maxBody)
			}
			if err == nil {
				t.Fatalf("accepted unsafe URL %s", raw)
			}
		}
	}
}

func TestExtractHTMLTextStripsActiveContent(t *testing.T) {
	got := extractHTMLText(`<style>leaked-css</style><script>secret-token</script><noscript>fallback</noscript><p>Visible <b>words</b></p>`)
	if got != "Visible words" {
		t.Fatalf("extracted %q", got)
	}
}

func TestPublicMediaLimitIsBounded(t *testing.T) {
	if _, _, err := GetPublicMedia(context.Background(), "https://example.com/", 0); err == nil {
		t.Fatal("zero media cap accepted")
	}
	if _, _, err := GetPublicMedia(context.Background(), "https://example.com/", MaxPublicMediaBytes+1); err == nil {
		t.Fatal("large media cap accepted")
	}
}

func TestNewValidatesEndpointWithoutServerCountQuota(t *testing.T) {
	if _, err := New([]ServerConfig{{ID: "bad", Transport: "http", URL: "http://example.com", Enabled: true}}); err == nil {
		t.Fatal("non-loopback HTTP accepted")
	}
	if _, err := New([]ServerConfig{{ID: "bad id", Transport: "stdio", Command: "x", Enabled: true}}); err == nil {
		t.Fatal("bad id accepted")
	}
	var many []ServerConfig
	for i := 0; i < 9; i++ {
		many = append(many, ServerConfig{ID: "s" + string(rune('a'+i)), Transport: "stdio", Command: "x", Enabled: true})
	}
	h, err := New(many)
	if err != nil {
		t.Fatal("server count quota retained", err)
	}
	h.Close()
}
func hasTool(ts []Tool, n string) bool {
	for _, t := range ts {
		if t.Name == n {
			return true
		}
	}
	return false
}
