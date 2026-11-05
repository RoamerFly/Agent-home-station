package mcptools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestHubHasNoRunnableBuiltinWebCapabilities(t *testing.T) {
	h, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	tools, err := h.List(t.Context())
	if err != nil || len(tools) != 0 || len(BuiltinTools()) != 0 {
		t.Fatal("legacy web tools remain discoverable", tools, err)
	}
	// A cancelled context also ensures a regression cannot contact a public
	// provider while this synthetic migration test diagnoses the old route.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	attempts := 0
	ctx = WithWebObserver(ctx, func(WebAttempt) { attempts++ })
	for _, call := range []struct{ name, args string }{
		{"web_search", `{"query":"synthetic query"}`},
		{"web_audio_search", `{"query":"synthetic song"}`},
		{"web_fetch", `{"url":"https://example.test/page"}`},
	} {
		out, err := h.Call(ctx, call.name, json.RawMessage(call.args))
		if out != "" || err == nil || err.Error() != "web_search_plugin_migration_required" {
			t.Fatal("legacy built-in did not fail closed", call.name, out, err)
		}
	}
	if attempts != 0 {
		t.Fatal("legacy built-in attempted public transport", attempts)
	}
}
