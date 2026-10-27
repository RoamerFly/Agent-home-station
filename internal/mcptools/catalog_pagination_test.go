package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMCPDiscoveryFollowsPagesBeyondFortyWithoutCachingCursorPages(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		t.Run(fmt.Sprint(cycle), func(t *testing.T) {
			requests := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req rpcRequest
				json.NewDecoder(r.Body).Decode(&req)
				if req.Method == "notifications/initialized" {
					w.WriteHeader(202)
					return
				}
				var result any = map[string]any{"protocolVersion": protocolVersion}
				if req.Method == "tools/list" {
					params, _ := req.Params.(map[string]any)
					cursor, _ := params["cursor"].(string)
					requests = append(requests, cursor)
					start := 0
					if cursor != "" {
						start = 30
					}
					tools := []map[string]any{}
					for i := start; i < start+30; i++ {
						tools = append(tools, map[string]any{"name": fmt.Sprintf("tool_%d", i), "inputSchema": map[string]string{"type": "object"}})
					}
					result = map[string]any{"tools": tools}
					if cursor == "" || cycle {
						result.(map[string]any)["nextCursor"] = "page2"
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			}))
			defer server.Close()
			h, e := New([]ServerConfig{{ID: "fixture", Enabled: true, Transport: "http", URL: server.URL}})
			if e != nil {
				t.Fatal(e)
			}
			defer h.Close()
			ctx := WithSessionScope(context.Background(), "catalog-pages")
			for run := 0; run < 2; run++ {
				tools, e := h.List(ctx)
				if cycle {
					if e == nil {
						t.Fatal("cursor cycle accepted")
					}
					return
				}
				if e != nil || len(tools) != len(builtin())+60 || !hasTool(tools, "fixture__tool_59") {
					t.Fatal("catalogue cut off", len(tools), e)
				}
			}
			if len(requests) != 3 || requests[0] != "" || requests[1] != "page2" || requests[2] != "page2" {
				t.Fatal("cursor pages incorrectly reused first-page cache", requests)
			}
		})
	}
}
