package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestTaskReasoningRequestsAreIsolatedAndRestoreDailySettings(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]map[string]any{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		messages := body["messages"].([]any)
		label := messages[1].(map[string]any)["content"].(string)
		mu.Lock()
		seen[label] = body
		mu.Unlock()
		w.Write([]byte(`{"choices":[{"message":{"content":"ok","reasoning_content":"synthetic"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	disabled := false
	c, err := NewWithAPIKey(Config{BaseURL: server.URL, ThinkingEnabled: &disabled, MaxTokens: 768, ToolMaxTokens: 4096}, "synthetic-task-key")
	if err != nil {
		t.Fatal(err)
	}
	c.http = server.Client()
	high, _ := WithTaskReasoning(t.Context(), TaskReasoning{Mode: "high", MaxTokens: 32768})
	maxMode, _ := WithTaskReasoning(t.Context(), TaskReasoning{Mode: "max"})
	reset, _ := WithTaskReasoning(high, TaskReasoning{Mode: "inherit"})
	var wg sync.WaitGroup
	for name, ctx := range map[string]context.Context{"high": high, "max": maxMode, "daily": t.Context(), "reset": reset} {
		wg.Add(1)
		go func(name string, ctx context.Context) {
			defer wg.Done()
			if _, err := c.ChatFunctions(ctx, "policy", []map[string]string{{"role": "user", "content": name}}, nil); err != nil {
				t.Error(err)
			}
		}(name, ctx)
	}
	wg.Wait()
	for name, budget := range map[string]int{"high": 32768, "max": 8192, "daily": 4096, "reset": 4096} {
		body := seen[name]
		if body == nil || body["max_tokens"] != float64(budget) {
			t.Fatalf("%s budget: %v", name, body)
		}
		typ := body["thinking"].(map[string]any)["type"]
		if name == "high" || name == "max" {
			if typ != "enabled" || body["reasoning_effort"] != name {
				t.Errorf("temporary profile missing: %v", body)
			}
		} else if typ != "disabled" {
			t.Errorf("temporary profile leaked into %s", name)
		}
	}
	if c.thinkingEnabled || c.maxTokens != 768 || c.toolMaxTokens != 4096 {
		t.Fatal("shared client mutated")
	}
}

func TestTaskReasoningRejectsUnsupportedProviderAndInvalidParameters(t *testing.T) {
	if _, err := WithTaskReasoning(t.Context(), TaskReasoning{Mode: "high", MaxTokens: 1}); err == nil {
		t.Fatal("invalid explicit budget accepted")
	}
	c := &Client{compatible: true}
	ctx, _ := WithTaskReasoning(t.Context(), TaskReasoning{Mode: "high"})
	if _, err := c.taskReasoningClient(ctx); err == nil {
		t.Fatal("unsupported setting silently ignored")
	}
}
