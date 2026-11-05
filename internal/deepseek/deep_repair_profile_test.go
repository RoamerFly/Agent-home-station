package deepseek

// The V10 evaluation profile matches the running application's distinct reply,
// tool and review budgets. This test makes local synthetic TLS requests only.
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeepRepairRuntimeProfileKeepsActualModelsAndBudgets(t *testing.T) {
	requests := make(chan map[string]any, 3)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- body
		_, _ = w.Write([]byte(`{"choices":[{"message":{"reasoning_content":"synthetic-hidden-reasoning","content":"{\"bubbles\":[]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":20}}`))
	}))
	defer server.Close()
	key := filepath.Join(t.TempDir(), "synthetic-key")
	if err := os.WriteFile(key, []byte("synthetic-profile-key-only"), 0600); err != nil {
		t.Fatal(err)
	}
	usage := []Usage{}
	client, err := New(Config{BaseURL: server.URL, KeyFile: key, Model: "deepseek-flash", MaxTokens: 768, ToolMaxTokens: 4096, OnUsage: func(u Usage) { usage = append(usage, u) }})
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = server.Client().Transport
	turns := []map[string]string{{"role": "user", "content": "safe synthetic input"}}
	if _, err = client.Chat(context.Background(), "synthetic policy", turns); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ChatFunctions(context.Background(), "synthetic policy", turns, nil); err != nil {
		t.Fatal(err)
	}
	answer, err := client.ChatGrounding(context.Background(), "synthetic JSON review", turns, GroundingOptions{Model: "deepseek-v4-pro", MaxTokens: 8192, Timeout: 120 * time.Second, ReasoningEffort: "high"})
	if err != nil || strings.Contains(answer, "synthetic-hidden-reasoning") {
		t.Fatal("review failed or hidden reasoning exposed", err)
	}
	for i, expected := range []struct {
		Model  string
		Tokens int
	}{{"deepseek-flash", 768}, {"deepseek-flash", 4096}, {"deepseek-v4-pro", 8192}} {
		body := <-requests
		if body["model"] != expected.Model || body["max_tokens"] != float64(expected.Tokens) {
			t.Fatal("runtime profile was ignored or clamped", i, body["model"], body["max_tokens"])
		}
		thinking := body["thinking"].(map[string]any)["type"]
		if i == 2 {
			if thinking != "enabled" || body["reasoning_effort"] != "high" || body["response_format"].(map[string]any)["type"] != "json_object" {
				t.Fatal("review transport lost declared profile")
			}
		} else if thinking != "disabled" || body["response_format"] != nil {
			t.Fatal("review profile changed normal generation")
		}
	}
	if len(usage) != 3 || usage[0].Model != "deepseek-flash" || usage[1].Model != "deepseek-flash" || usage[2].Model != "deepseek-v4-pro" {
		t.Fatal("actual-model usage was attributed to a different model", usage)
	}
	if client.model != "deepseek-flash" || client.maxTokens != 768 || client.toolMaxTokens != 4096 {
		t.Fatal("independent review mutated main client")
	}
}

func TestDeepRepairExplicitV11ReviewProfileBody(t *testing.T) {
	requests := make(chan map[string]any, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- body
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"bubbles\":[]}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	key := filepath.Join(t.TempDir(), "synthetic-key")
	if err := os.WriteFile(key, []byte("synthetic-profile-key-only"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{BaseURL: server.URL, KeyFile: key, Model: "deepseek-flash", MaxTokens: 768, ToolMaxTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = server.Client().Transport
	_, err = client.ChatGrounding(t.Context(), "synthetic JSON review", []map[string]string{{"role": "user", "content": "safe synthetic input"}}, GroundingOptions{Model: "deepseek-v4-pro", MaxTokens: 32768, Timeout: 240 * time.Second, ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	body := <-requests
	if body["model"] != "deepseek-v4-pro" || body["max_tokens"] != float64(32768) || body["reasoning_effort"] != "high" || body["thinking"].(map[string]any)["type"] != "enabled" || body["response_format"].(map[string]any)["type"] != "json_object" {
		t.Fatal("actual V11 HTTP body did not preserve review profile", body["model"], body["max_tokens"])
	}
	if client.maxTokens != 768 || client.toolMaxTokens != 4096 || client.http.Timeout != 90*time.Second {
		t.Fatal("independent V11 review mutated primary generation client")
	}
}
