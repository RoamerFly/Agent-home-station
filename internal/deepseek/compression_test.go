package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCompressionOutputBudgetDoesNotChangeNormalReplies(t *testing.T) {
	var budgets []int
	var jsonModes []bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int               `json:"max_tokens"`
			Format    map[string]string `json:"response_format"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		budgets = append(budgets, body.MaxTokens)
		jsonModes = append(jsonModes, body.Format["type"] == "json_object")
		if body.MaxTokens == 4096 {
			w.Write([]byte(`{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}]}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	key := filepath.Join(t.TempDir(), "key")
	os.WriteFile(key, []byte("synthetic-test-key"), 0600)
	client, err := New(Config{BaseURL: server.URL, KeyFile: key, MaxTokens: 768})
	if err != nil {
		t.Fatal(err)
	}
	client.http = server.Client()
	turns := []map[string]string{{"role": "user", "content": "synthetic input"}}
	client.Chat(context.Background(), "chat policy", turns)
	client.ChatCompression(context.Background(), "summary policy", turns)
	client.Chat(context.Background(), "chat policy", turns)
	if len(budgets) != 4 || budgets[0] != 768 || budgets[1] != 4096 || budgets[2] != 8192 || budgets[3] != 768 || jsonModes[0] || !jsonModes[1] || !jsonModes[2] || jsonModes[3] {
		t.Fatal("shared reply budget changed", budgets, jsonModes)
	}
}

func TestCompressionCompatibleProviderWithoutJSONMode(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["response_format"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body["max_tokens"] != float64(4096) {
			t.Error("fallback lost compression budget")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	key := filepath.Join(t.TempDir(), "key")
	os.WriteFile(key, []byte("synthetic-test-key"), 0600)
	client, err := New(Config{BaseURL: server.URL, KeyFile: key, Compatible: true, MaxTokens: 768})
	if err != nil {
		t.Fatal(err)
	}
	client.http = server.Client()
	if _, err := client.ChatCompression(context.Background(), "summary policy", []map[string]string{{"role": "user", "content": "synthetic input"}}); err != nil || requests != 2 {
		t.Fatal("compatible compression did not recover", err, requests)
	}
}
