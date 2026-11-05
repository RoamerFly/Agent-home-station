package mcptools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestPersistentMCPChild(t *testing.T) {
	if os.Getenv("WECHAT_MCP_SESSION_CHILD") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	count := 0
	for scanner.Scan() {
		var req rpcRequest
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(2)
		}
		switch req.Method {
		case "initialize":
			fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"serverInfo\":{\"name\":\"fixture\",\"version\":\"1\"}}}\n", req.ID)
		case "tools/list":
			fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"tools\":[{\"name\":\"increment\",\"inputSchema\":{\"type\":\"object\"}}]}}\n", req.ID)
		case "tools/call":
			params, _ := req.Params.(map[string]any)
			if params["name"] == "block" {
				time.Sleep(time.Hour)
			}
			count++
			value := strconv.Itoa(count)
			if params["name"] == "ambient_env" {
				value = os.Getenv("WECHAT_MCP_AMBIENT_SECRET")
			}
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"content": []map[string]string{{"type": "text", "text": value}}}})
			fmt.Println(string(b))
		}
	}
	os.Exit(0)
}

func TestScopedStdioCancellationClosesSessionAndDoesNotInheritProviderEnvironment(t *testing.T) {
	t.Setenv("WECHAT_MCP_AMBIENT_SECRET", "sensitive-fixture")
	h, err := New([]ServerConfig{{ID: "fixture", Enabled: true, Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestPersistentMCPChild$"}, Env: map[string]string{"WECHAT_MCP_SESSION_CHILD": "1"}}}, WithStdioLauncher(syntheticStdioLauncher))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ctx := WithSessionScope(context.Background(), "cancel-room")
	if _, err = h.List(ctx); err != nil {
		t.Fatal(err)
	}
	secret, err := h.Call(ctx, "fixture__ambient_env", json.RawMessage(`{}`))
	if err != nil || secret != "" {
		t.Fatal("ambient credential inherited", err)
	}
	short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err = h.Call(short, "fixture__block", json.RawMessage(`{}`)); err == nil || time.Since(start) > 5*time.Second {
		t.Fatal("cancel did not release transport", err)
	}
	value, err := h.Call(ctx, "fixture__increment", json.RawMessage(`{}`))
	if err != nil || value != "1" {
		t.Fatal("cancelled session reused or operation replayed", err, value)
	}
}

func TestScopedMCPSessionPersistsAndIsolatesConversations(t *testing.T) {
	hub, err := New([]ServerConfig{{ID: "fixture", Enabled: true, Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestPersistentMCPChild$"}, Env: map[string]string{"WECHAT_MCP_SESSION_CHILD": "1"}}}, WithStdioLauncher(syntheticStdioLauncher))
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	roomA := WithSessionScope(context.Background(), "scope-A")
	roomB := WithSessionScope(context.Background(), "scope-B")
	if _, err = hub.List(roomA); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ctx  context.Context
		want string
	}{{roomA, "1"}, {roomA, "2"}, {roomB, "1"}, {roomA, "3"}} {
		got, err := hub.Call(tc.ctx, "fixture__increment", json.RawMessage(`{}`))
		if err != nil || got != tc.want {
			t.Fatal("session lost or crossed scopes", err, got, tc.want)
		}
	}
	if len(hub.sessions) != 2 {
		t.Fatal("incorrect session count", len(hub.sessions))
	}
	hub.Close()
	if _, err = hub.Call(roomA, "fixture__increment", json.RawMessage(`{}`)); err == nil {
		t.Fatal("closed runtime restarted process")
	}
}

func TestScopedHTTPSessionsCacheDiscoveryAndNeverReplayFailedCall(t *testing.T) {
	var mu sync.Mutex
	initializations, lists, calls := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var req rpcRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid rpc")
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			initializations++
			w.Header().Set("Mcp-Session-Id", fmt.Sprintf("scope-%d", initializations))
			result = map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}}
		case "notifications/initialized":
			w.WriteHeader(202)
			return
		case "tools/list":
			lists++
			if r.Header.Get("Mcp-Session-Id") == "" {
				t.Error("missing session")
			}
			result = map[string]any{"tools": []map[string]any{{"name": "work", "inputSchema": map[string]string{"type": "object"}}}}
		case "tools/call":
			calls++
			http.Error(w, "interrupted after possibly executing", 503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()
	h, err := New([]ServerConfig{{ID: "fixture", Enabled: true, Transport: "http", URL: server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ctx := WithSessionScope(context.Background(), "room-a")
	for i := 0; i < 2; i++ {
		if _, err = h.List(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if initializations != 1 || lists != 1 {
		t.Fatal("discovery did not reuse session/cache", initializations, lists)
	}
	if _, err = h.Call(ctx, "fixture__work", json.RawMessage(`{}`)); err == nil {
		t.Fatal("failed call accepted")
	}
	if calls != 1 {
		t.Fatal("uncertain operation replayed", calls)
	}
	if _, err = h.List(ctx); err != nil {
		t.Fatal(err)
	}
	if initializations != 2 {
		t.Fatal("broken session not renewed for later discovery", initializations)
	}
}
