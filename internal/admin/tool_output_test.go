package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRetainedMemoryPageInvalidatedAfterSourceChange(t *testing.T) {
	_, p := trustedAgent(t)
	p.manager.mu.Lock()
	p.manager.config.Companion.Permissions.Remember = true
	p.manager.mu.Unlock()
	ctx := t.Context()
	epoch, err := p.manager.memory.DataEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.stampMemoryQuery(ctx, epoch, p.memoryQueryAuthorization(), map[string]any{"text": strings.Repeat("synthetic private memory ", 500)})
	if err != nil {
		t.Fatal(err)
	}
	id, err := p.RetainToolOutput(ctx, "memory_read", result)
	if err != nil || id == "" {
		t.Fatal(id, err)
	}
	in := p.trace
	in.Text = "new source revision"
	in.TraceID = "synthetic-change"
	c := p.manager.Config().Companion
	c.Permissions.Remember = true
	if err = p.manager.memory.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err = p.readToolOutput(ctx, "", json.RawMessage(`{"result_id":"`+id+`"}`))
	if err == nil || !strings.Contains(err.Error(), "memory_result_invalidated") {
		t.Fatal("stale private page remained readable", err)
	}
}

func TestMemoryResultCannotBeRestampedAfterConcurrentChange(t *testing.T) {
	_, p := trustedAgent(t)
	p.manager.mu.Lock()
	p.manager.config.Companion.Permissions.Remember = true
	p.manager.mu.Unlock()
	ctx := t.Context()
	epoch, err := p.manager.memory.DataEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldResult, err := p.stampMemoryQuery(ctx, epoch, p.memoryQueryAuthorization(), map[string]any{"text": "synthetic old source"})
	if err != nil {
		t.Fatal(err)
	}
	in := p.trace
	in.Text = "different source"
	in.TraceID = "synthetic-race"
	c := p.manager.Config().Companion
	c.Permissions.Remember = true
	if err = p.manager.memory.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = p.stampMemoryQuery(ctx, epoch, p.memoryQueryAuthorization(), map[string]any{"text": "synthetic old source"}); err == nil {
		t.Fatal("read crossing a source change was stamped as current")
	}
	for _, name := range []string{"memory_read", "memory_search", "agent_global_search"} {
		if id, err := p.RetainToolOutput(ctx, name, oldResult); err == nil || id != "" {
			t.Fatalf("%s relabelled stale query: %s %v", name, id, err)
		}
		if id, err := p.RetainToolOutput(ctx, name, `{"text":"unstamped legacy source"}`); err == nil || id != "" {
			t.Fatalf("%s retained unstamped source: %s %v", name, id, err)
		}
	}
}

func TestToolOutputPaginationIsIsolatedByVerifiedSpeakerAndConversation(t *testing.T) {
	_, p := trustedAgent(t)
	installProjectPluginTestRuntime(t, p)
	ctx := context.Background()
	full := strings.Repeat("中文结果\n", 3000)
	id, err := p.RetainToolOutput(ctx, "server_read", full)
	if err != nil || id == "" {
		t.Fatal(id, err)
	}
	args := json.RawMessage(`{"result_id":"` + id + `","offset":5,"max_chars":120}`)
	result, err := p.Call(ctx, "tool_output_read", args)
	var page struct {
		Text     string
		Next     int `json:"next_offset"`
		Complete bool
	}
	if err != nil || json.Unmarshal([]byte(result), &page) != nil || len([]rune(page.Text)) != 120 || page.Next != 125 || page.Complete {
		t.Fatal("pagination failed", result, err)
	}
	trace := p.trace
	longArgs := json.RawMessage(`{"result_id":"` + id + `","max_chars":4000}`)
	longResult, e := p.Call(ctx, "tool_output_read", longArgs)
	var longPage struct {
		Text string
		Next int `json:"next_offset"`
	}
	if e != nil || len(longResult) > 5400 || json.Unmarshal([]byte(longResult), &longPage) != nil || longPage.Next != len([]rune(longPage.Text)) {
		t.Fatal("multibyte pagination skipped hidden text", e)
	}
	p.trace.Sender = "different-member"
	if _, err = p.Call(ctx, "tool_output_read", args); err == nil {
		t.Fatal("other member read owner tool output")
	}
	p.trace = trace
	p.trace.Preview = true
	if _, e = p.Call(ctx, "tool_output_read", args); e == nil {
		t.Fatal("preview read real retained output")
	}
	p.trace = trace
	p.trace.Thread = "different-group"
	if _, err = p.Call(ctx, "tool_output_read", args); err == nil {
		t.Fatal("other group read output")
	}
	p.trace = trace
	stored := retainedToolOutput{Created: time.Now().Add(-8 * 24 * time.Hour), Result: "expired"}
	data, _ := json.Marshal(stored)
	if err = os.WriteFile(filepath.Join(p.outputScope(), id+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Call(ctx, "tool_output_read", args); err == nil {
		t.Fatal("expired result read")
	}
	if _, err = p.readToolOutput(ctx, "", json.RawMessage(`{"result_id":"../../secret"}`)); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestCapabilitiesHaveOneHandlerPerAdvertisedTool(t *testing.T) {
	_, p := trustedAgent(t)
	seen := map[string]bool{}
	modules := p.capabilities()
	for _, module := range modules {
		if module.id == "" || module.list == nil || module.call == nil {
			t.Fatal("incomplete registration")
		}
		for _, tool := range module.list() {
			if seen[tool.Name] {
				t.Fatal("duplicate capability", tool.Name)
			}
			seen[tool.Name] = true
			count := 0
			for _, handler := range modules {
				if handler.match(tool.Name) {
					count++
				}
			}
			if count != 1 || !module.match(tool.Name) {
				t.Fatal("discovery and invocation disagree", module.id, tool.Name, count)
			}
		}
	}
}
