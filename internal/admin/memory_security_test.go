package admin

import (
	"encoding/json"
	"os"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestMemoryStartupHoldMustVerifyBeforeActivation(t *testing.T) {
	f := setup(t)
	f.login()
	hold := f.manager.memoryUpdateHoldPath()
	if e := os.WriteFile(hold, []byte(`{"synthetic":true}`), 0600); e != nil {
		t.Fatal(e)
	}
	if !f.manager.draining() {
		t.Fatal("persistent migration hold ignored")
	}
	code, _, _ := f.request("POST", "/api/runtime/memory-activate", map[string]any{}, true, false)
	if code != 403 {
		t.Fatal("activation did not require CSRF", code)
	}
	if !f.manager.draining() {
		t.Fatal("unauthorized activation released hold")
	}
	code, raw, _ := f.request("POST", "/api/runtime/memory-activate", map[string]any{}, true, true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if f.manager.draining() {
		t.Fatal("activation did not release hold")
	}
}

func TestGlobalSearchCursorRelevanceAndRevocation(t *testing.T) {
	f, p := globalSearchFixture(t)
	cfg := f.manager.config.Companion
	for i, text := range []string{"苏州红茶 老故事", "苏州红茶 新约定", "青岛咖啡"} {
		in := companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", Sender: "member", TraceID: string(rune('a' + i)), Text: text}
		if e := f.manager.memory.Observe(in, cfg, time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	raw, e := p.Call(t.Context(), "agent_global_search", json.RawMessage(`{"query":"苏州 红茶","order":"relevance","limit":1}`))
	if e != nil {
		t.Fatal(e)
	}
	var page struct {
		NextCursor string `json:"next_cursor"`
		Total      int
		Items      []map[string]any
	}
	if e = json.Unmarshal([]byte(raw), &page); e != nil || page.Total != 2 || page.NextCursor == "" {
		t.Fatal(raw, e)
	}
	next, _ := json.Marshal(map[string]any{"query": "苏州 红茶", "order": "relevance", "limit": 1, "cursor": page.NextCursor})
	if _, e = p.Call(t.Context(), "agent_global_search", next); e != nil {
		t.Fatal(e)
	}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", Sender: "member", TraceID: "new", Text: "新的苏州红茶资料"}
	f.manager.memory.Observe(in, cfg, time.Now())
	if _, e = p.Call(t.Context(), "agent_global_search", next); e == nil {
		t.Fatal("changed catalog accepted stale cursor")
	}
	p.trace.Sender = "member"
	if _, e = p.Call(t.Context(), "agent_global_search", json.RawMessage(`{}`)); e == nil {
		t.Fatal("revoked owner inherited cursor access")
	}
}
