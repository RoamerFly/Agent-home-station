package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
		if body.MaxTokens == 16384 {
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
	if len(budgets) != 4 || budgets[0] != 768 || budgets[1] != 16384 || budgets[2] != 32768 || budgets[3] != 768 || jsonModes[0] || !jsonModes[1] || !jsonModes[2] || jsonModes[3] {
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
		if body["max_tokens"] != float64(16384) {
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

func TestCompressionHTTP200LongResponseUsesBoundedContinuation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("request body was not JSON")
		}
		if body.MaxTokens == 16384 {
			// Simulate a long structured response cut off at the provider token
			// limit. The client must request the compression budget once more.
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + strings.Repeat("x", 20000) + `"},"finish_reason":"length"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("synthetic-test-key"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{BaseURL: server.URL, KeyFile: key, MaxTokens: 768})
	if err != nil {
		t.Fatal(err)
	}
	client.http = server.Client()
	answer, err := client.ChatCompression(context.Background(), "summary policy", []map[string]string{{"role": "user", "content": "synthetic input"}})
	if err != nil || answer != "{}" || calls.Load() != 2 {
		t.Fatalf("compression continuation failed: answer=%q err=%v calls=%d", answer, err, calls.Load())
	}
}

type timeoutReadError struct{}

func (timeoutReadError) Error() string   { return "synthetic timeout" }
func (timeoutReadError) Timeout() bool   { return true }
func (timeoutReadError) Temporary() bool { return true }

type timeoutBody struct {
	first bool
}

func (b *timeoutBody) Read(p []byte) (int, error) {
	if b.first {
		b.first = false
	}
	return 0, timeoutReadError{}
}

func (b *timeoutBody) Close() error { return nil }

func TestCompressionHTTP200BodyTimeoutRetriesAndCompletes(t *testing.T) {
	var calls atomic.Int32
	client := &Client{endpoint: "https://synthetic.invalid/chat/completions", key: "synthetic-key", model: "deepseek-flash", maxTokens: 16384, http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &timeoutBody{}}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))}, nil
	})}}
	answer, err := client.ChatCompression(context.Background(), "summary policy", []map[string]string{{"role": "user", "content": "synthetic input"}})
	if err != nil || answer != "{}" || calls.Load() != 2 {
		d, _ := ErrorDiagnostics(err)
		t.Fatalf("timeout continuation failed: answer=%q err=%v diag=%+v calls=%d", answer, err, d, calls.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCompressionHTTP200TimeoutEnvelopeIsRetryable(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"error":{"code":"upstream_timeout"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	client := &Client{endpoint: server.URL, key: "synthetic-key", model: "deepseek-flash", maxTokens: 16384, http: server.Client()}
	answer, err := client.ChatCompression(context.Background(), "summary policy", []map[string]string{{"role": "user", "content": "synthetic input"}})
	if err != nil || answer != "{}" || calls.Load() != 2 {
		t.Fatalf("HTTP 200 timeout envelope did not recover: answer=%q err=%v calls=%d", answer, err, calls.Load())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("provider timeout was mistaken for caller deadline")
	}
}
