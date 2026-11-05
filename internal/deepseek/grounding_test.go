package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func groundingTestClient(t *testing.T, server *httptest.Server, compatible bool, usage func(Usage)) *Client {
	t.Helper()
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("synthetic-grounding-test-key"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{BaseURL: server.URL, KeyFile: key, Model: "configured-model", MaxTokens: 768, Compatible: compatible, OnUsage: usage})
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = server.Client().Transport
	return client
}

func TestGroundingUsesIndependentJSONThinkingBudgetAndReportedUsage(t *testing.T) {
	requests := make(chan map[string]any, 3)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- body
		w.Write([]byte(`{"choices":[{"message":{"reasoning_content":"synthetic-private-reasoning","content":"{\"bubbles\":[]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":350,"prompt_cache_hit_tokens":80,"completion_tokens_details":{"reasoning_tokens":300}}}`))
	}))
	defer server.Close()
	var usages []Usage
	client := groundingTestClient(t, server, false, func(u Usage) { usages = append(usages, u) })
	turns := []map[string]string{{"role": "user", "content": "synthetic candidate"}}
	for _, purpose := range []string{"reply_grounding_check", "reply_grounding_repair"} {
		answer, err := client.ChatGrounding(WithPurpose(context.Background(), purpose), "read-only JSON policy", turns, GroundingOptions{})
		if err != nil || answer != `{"bubbles":[]}` || strings.Contains(answer, "synthetic-private-reasoning") {
			t.Fatalf("answer=%q err=%v", answer, err)
		}
		body := <-requests
		if body["model"] != "configured-model" || body["max_tokens"] != float64(8192) || body["reasoning_effort"] != "high" || body["stream"] != false {
			t.Fatalf("wrong review configuration: %v", body)
		}
		if body["response_format"].(map[string]any)["type"] != "json_object" || body["thinking"].(map[string]any)["type"] != "enabled" {
			t.Fatal("review did not use JSON/thinking")
		}
		if _, ok := body["tools"]; ok {
			t.Fatal("read-only review exposed tools")
		}
	}
	if _, err := client.Chat(context.Background(), "normal chat policy", turns); err != nil {
		t.Fatal(err)
	}
	normal := <-requests
	if normal["max_tokens"] != float64(768) || normal["thinking"].(map[string]any)["type"] != "disabled" {
		t.Fatal("review changed normal chat configuration")
	}
	if _, ok := normal["response_format"]; ok {
		t.Fatal("review JSON mode leaked to normal chat")
	}
	if _, ok := normal["reasoning_effort"]; ok {
		t.Fatal("review effort leaked to normal chat")
	}
	if client.http.Timeout != 90*time.Second || client.maxTokens != 768 {
		t.Fatal("shared client mutated")
	}
	if len(usages) != 3 || usages[0].Model != "configured-model" || usages[1].Model != "configured-model" || usages[2].Model != "configured-model" || usages[0].Purpose != "reply_grounding_check" || usages[1].Purpose != "reply_grounding_repair" || !usages[0].Reported || usages[0].Input != 120 || usages[0].Output != 350 || usages[0].CacheHit != 80 {
		t.Fatalf("provider usage/purpose lost: %+v", usages)
	}
}

func TestGroundingExplicitOptionsAreNotClamped(t *testing.T) {
	defaults, err := (GroundingOptions{}).Normalized()
	if err != nil || defaults.MaxTokens != 8192 || defaults.Timeout != 120*time.Second || defaults.ReasoningEffort != "high" {
		t.Fatalf("wrong default review options: %+v err=%v", defaults, err)
	}
	requests := make(chan map[string]any, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		requests <- body
		w.Write([]byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	client := groundingTestClient(t, server, false, nil)
	options := GroundingOptions{MaxTokens: 16384, Timeout: 180 * time.Second, ReasoningEffort: "max"}
	if _, err := client.ChatGrounding(context.Background(), "JSON policy", []map[string]string{{"role": "user", "content": "synthetic"}}, options); err != nil {
		t.Fatal(err)
	}
	body := <-requests
	if body["max_tokens"] != float64(options.MaxTokens) || body["reasoning_effort"] != options.ReasoningEffort {
		t.Fatal("explicit review options silently clamped")
	}
	for _, tokens := range []int{1, 393216} {
		if got, err := (GroundingOptions{MaxTokens: tokens}).Normalized(); err != nil || got.MaxTokens != tokens {
			t.Fatalf("documented provider boundary rejected: tokens=%d err=%v", tokens, err)
		}
	}
	for _, bad := range []GroundingOptions{{MaxTokens: -1}, {MaxTokens: 393217}, {Timeout: -time.Second}, {ReasoningEffort: "none"}, {ReasoningEffort: "arbitrary"}, {Model: strings.Repeat("x", 129)}, {Model: " "}, {Model: "model name"}, {Model: "\tmodel"}, {Model: "model\n"}, {Model: "model\x00name"}, {Model: "model\u2003name"}, {Model: string([]byte{0xff})}} {
		if _, err := bad.Normalized(); err == nil {
			t.Fatalf("invalid options accepted: %+v", bad)
		}
	}
}

func TestGroundingReviewModelDoesNotChangeChatOrCompressionModel(t *testing.T) {
	requests := make(chan map[string]any, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- body
		if r.Header.Get("Authorization") != "Bearer synthetic-grounding-test-key" || r.URL.Path != "/chat/completions" {
			t.Error("model override changed credential or endpoint")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":34}}`))
	}))
	defer server.Close()
	var usages []Usage
	client := groundingTestClient(t, server, false, func(u Usage) { usages = append(usages, u) })
	turns := []map[string]string{{"role": "user", "content": "synthetic"}}
	options := GroundingOptions{Model: "deepseek-v4-pro"}
	if _, err := client.ChatGrounding(context.Background(), "JSON review policy", turns, options); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Chat(context.Background(), "normal chat", turns); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ChatCompression(context.Background(), "JSON summary policy", turns); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ChatGrounding(context.Background(), "JSON review policy", turns, GroundingOptions{}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"deepseek-v4-pro", "configured-model", "configured-model", "configured-model"} {
		body := <-requests
		if body["model"] != want || usages[i].Model != want || !usages[i].Reported || usages[i].Input != 12 || usages[i].Output != 34 {
			t.Fatalf("request/usage[%d] lost actual model: request=%v usage=%+v", i, body, usages[i])
		}
	}
	if client.model != "configured-model" {
		t.Fatal("review model changed shared client")
	}
}

func TestGroundingCompatibleJSONFailureDoesNotFallback(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, exists := body["thinking"]; exists {
			t.Error("DeepSeek thinking field leaked to compatible provider")
		}
		if _, exists := body["reasoning_effort"]; exists {
			t.Error("DeepSeek effort field leaked to compatible provider")
		}
		if body["response_format"].(map[string]any)["type"] != "json_object" {
			t.Error("structured review missing JSON mode")
		}
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"synthetic-private-error"}}`))
	}))
	defer server.Close()
	client := groundingTestClient(t, server, true, nil)
	_, err := client.ChatGrounding(context.Background(), "JSON policy", []map[string]string{{"role": "user", "content": "synthetic"}}, GroundingOptions{})
	d, ok := ErrorDiagnostics(err)
	if !ok || d.Code != "model_invalid_parameters" || d.Attempts != 1 || requests.Load() != 1 || strings.Contains(err.Error(), "synthetic-private-error") {
		t.Fatalf("JSON failure silently downgraded or leaked: err=%v requests=%d", err, requests.Load())
	}
}

func TestGroundingTimeoutAndParentCancellationBoundTheWholeRequest(t *testing.T) {
	for _, tc := range []struct {
		name          string
		reviewTimeout time.Duration
		parentTimeout time.Duration
	}{
		{"review", 80 * time.Millisecond, 2 * time.Second},
		{"parent", 2 * time.Second, 80 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				select {
				case <-r.Context().Done():
				case <-time.After(2 * time.Second):
				}
			}))
			defer server.Close()
			client := groundingTestClient(t, server, false, nil)
			ctx, cancel := context.WithTimeout(context.Background(), tc.parentTimeout)
			defer cancel()
			start := time.Now()
			answer, err := client.ChatGrounding(ctx, "JSON policy", []map[string]string{{"role": "user", "content": "synthetic"}}, GroundingOptions{Timeout: tc.reviewTimeout})
			d, ok := ErrorDiagnostics(err)
			if !ok || d.Code != "model_deadline_exceeded" || d.Attempts != 1 || answer != "" || time.Since(start) > time.Second || requests.Load() != 1 {
				t.Fatalf("review timeout not respected: answer=%q err=%v requests=%d duration=%s", answer, err, requests.Load(), time.Since(start))
			}
			if client.http.Timeout != 90*time.Second {
				t.Fatal("review timeout changed shared client")
			}
		})
	}
}

func TestGroundingRejectsTruncationInvalidContentAndToolCalls(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
	}{
		{"truncated", `{"choices":[{"message":{"reasoning_content":"synthetic-private-reasoning","content":"{\"partial\":"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":8192}}`, "model_output_token_limit"},
		{"nonjson", `{"choices":[{"message":{"content":"ordinary prose"},"finish_reason":"stop"}]}`, "model_invalid_response"},
		{"tool", `{"choices":[{"message":{"tool_calls":[{"function":{"name":"do_something","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, "model_invalid_response"},
		{"hidden_tool", `{"choices":[{"message":{"content":"{}","tool_calls":[{"function":{"name":"do_something","arguments":"{}"}}]},"finish_reason":"stop"}]}`, "model_invalid_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			var usage Usage
			client := groundingTestClient(t, server, false, func(u Usage) { usage = u })
			answer, err := client.ChatGrounding(context.Background(), "JSON policy", []map[string]string{{"role": "user", "content": "synthetic"}}, GroundingOptions{})
			d, ok := ErrorDiagnostics(err)
			wantRequests := int32(1)
			if tc.name == "nonjson" {
				wantRequests = 2
			}
			if !ok || d.Code != tc.code || answer != "" || requests.Load() != wantRequests || strings.Contains(err.Error(), "synthetic-private-reasoning") {
				t.Fatalf("bad response accepted or leaked: answer=%q err=%v requests=%d", answer, err, requests.Load())
			}
			if tc.name == "nonjson" && (d.FormatAttempt != 2 || d.MaxFormatAttempts != 2 || d.FinishReason != "stop" || d.ContentBytes != len("ordinary prose") || d.JSONValid || d.Attempts != 1) {
				t.Fatalf("invalid final JSON lost bounded format diagnostics: %+v", d)
			}
			if tc.name == "truncated" && (d.FinishReason != "length" || d.FormatAttempt != 1 || d.JSONValid) {
				t.Fatalf("truncation lost finish metadata or triggered format retry: %+v", d)
			}
			if tc.name == "truncated" && (!usage.Reported || usage.Output != 8192 || usage.Purpose != "reply_grounding_check") {
				t.Fatalf("billed truncated response lost usage: %+v", usage)
			}
		})
	}
}

func TestGroundingFormatRepairKeepsCandidateAndAccountsEveryAttempt(t *testing.T) {
	const invalid = "synthetic-private-invalid-final"
	const valid = `{"bubbles":[{"index":0,"verdict":"reject","violations":[]}]}`
	requests := make(chan map[string]any, 2)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests <- request
		content := invalid
		if calls.Add(1) == 2 {
			content = valid
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content, "reasoning_content": "synthetic-private-reasoning"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 25, "completion_tokens": 77}})
	}))
	defer server.Close()
	var usages []Usage
	client := groundingTestClient(t, server, false, func(u Usage) { usages = append(usages, u) })
	var responses []Diagnostics
	var repairs []Diagnostics
	const originalContent = `{"candidate":{"messages":["synthetic fixed draft"]},"sources":[{"text":"synthetic fixed evidence"}]}`
	turns := []map[string]string{{"role": "user", "content": originalContent}}
	ctx := WithRequestObserver(WithPurpose(context.Background(), "reply_grounding_final_check"), func(event string, d Diagnostics) {
		switch event {
		case "model_grounding_response":
			responses = append(responses, d)
			if d.FormatAttempt == 1 {
				turns[0]["content"] = "synthetic caller mutation must not replace the reviewed evidence"
			}
		case "model_grounding_format_repair":
			repairs = append(repairs, d)
		}
	})
	answer, err := client.ChatGrounding(ctx, "trusted JSON policy", turns, GroundingOptions{Model: "deepseek-v4-pro", MaxTokens: 32768})
	if err != nil || answer != valid || calls.Load() != 2 {
		t.Fatalf("format repair failed: answer=%q err=%v calls=%d", answer, err, calls.Load())
	}
	first, second := <-requests, <-requests
	firstTurns, secondTurns := first["messages"].([]any), second["messages"].([]any)
	firstEvidence, _ := json.Marshal(firstTurns[1:])
	secondEvidence, _ := json.Marshal(secondTurns[1:])
	if len(firstTurns) != 2 || len(secondTurns) != 2 || string(firstEvidence) != string(secondEvidence) || firstTurns[1].(map[string]any)["content"] != originalContent {
		t.Fatal("format repair changed candidate or evidence")
	}
	if firstTurns[0].(map[string]any)["content"] != "trusted JSON policy" || !strings.HasPrefix(secondTurns[0].(map[string]any)["content"].(string), "trusted JSON policy\n") {
		t.Fatal("format repair changed the original trusted policy")
	}
	secondBody, _ := json.Marshal(second)
	if strings.Contains(string(secondBody), invalid) || strings.Contains(string(secondBody), "synthetic-private-reasoning") {
		t.Fatal("format repair re-injected invalid content or hidden reasoning")
	}
	for _, request := range []map[string]any{first, second} {
		if request["model"] != "deepseek-v4-pro" || request["max_tokens"] != float64(32768) || request["reasoning_effort"] != "high" || request["response_format"].(map[string]any)["type"] != "json_object" || request["thinking"].(map[string]any)["type"] != "enabled" {
			t.Fatal("format retry changed explicit model, budget, or protocol")
		}
	}
	if len(usages) != 2 || usages[0].Purpose != "reply_grounding_final_check" || usages[1].Purpose != "reply_grounding_final_check_format_repair" {
		t.Fatalf("format attempt usage not separately visible: %+v", usages)
	}
	for _, u := range usages {
		if u.Model != "deepseek-v4-pro" || !u.Reported || u.Input != 25 || u.Output != 77 {
			t.Fatalf("format attempt lost actual model or billed tokens: %+v", u)
		}
	}
	if len(responses) != 2 || len(repairs) != 1 || responses[0].FinishReason != "stop" || responses[0].ContentBytes != len(invalid) || responses[0].JSONValid || responses[0].FormatAttempt != 1 || responses[0].Attempts != 1 || responses[0].Code != "model_invalid_response" || !responses[1].JSONValid || responses[1].FormatAttempt != 2 || responses[1].Code != "" {
		t.Fatalf("safe response diagnostics lost: responses=%+v repairs=%+v", responses, repairs)
	}
	diagnosticJSON, _ := json.Marshal(responses)
	if strings.Contains(string(diagnosticJSON), invalid) || strings.Contains(string(diagnosticJSON), "synthetic-private-reasoning") {
		t.Fatal("safe response diagnostics leaked model content")
	}
}

func TestGroundingValidSemanticVerdictsAreNotResampled(t *testing.T) {
	for _, verdict := range []string{"allow", "reject", "uncertain"} {
		t.Run(verdict, func(t *testing.T) {
			var calls atomic.Int32
			content := `{"bubbles":[{"index":0,"verdict":"` + verdict + `","violations":[]}]}`
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": "stop"}}})
			}))
			defer server.Close()
			client := groundingTestClient(t, server, false, nil)
			answer, err := client.ChatGrounding(context.Background(), "JSON policy", []map[string]string{{"role": "user", "content": "synthetic fixed candidate"}}, GroundingOptions{})
			if err != nil || answer != content || calls.Load() != 1 {
				t.Fatalf("valid semantic verdict was resampled: answer=%q err=%v calls=%d", answer, err, calls.Load())
			}
		})
	}
}

func TestGroundingFormatRepairSharesTheOriginalDeadline(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if calls.Add(1) == 1 {
			w.Write([]byte(`{"choices":[{"message":{"content":"synthetic invalid JSON"},"finish_reason":"stop"}]}`))
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()
	client := groundingTestClient(t, server, false, nil)
	deadlines := make(chan time.Time, 2)
	originalTransport := client.http.Transport
	client.http.Transport = recoveryTransport(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Error("review request lacks deadline")
		}
		deadlines <- deadline
		return originalTransport.RoundTrip(r)
	})
	start := time.Now()
	answer, err := client.ChatGrounding(context.Background(), "JSON policy", []map[string]string{{"role": "user", "content": "synthetic"}}, GroundingOptions{Timeout: 120 * time.Millisecond})
	d, ok := ErrorDiagnostics(err)
	if !ok || d.Code != "model_deadline_exceeded" || d.FormatAttempt != 2 || calls.Load() != 2 || answer != "" || time.Since(start) > time.Second {
		t.Fatalf("format repair escaped original timeout: answer=%q err=%v calls=%d", answer, err, calls.Load())
	}
	first, second := <-deadlines, <-deadlines
	if !first.Equal(second) {
		t.Fatalf("format repair renewed timeout: first=%s second=%s", first, second)
	}
}

func TestGroundingEnvelopeAllowsBoundedReasoningWithoutReturningIt(t *testing.T) {
	largeReasoning := strings.Repeat("synthetic ", 30000)
	body, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{}`, "reasoning_content": largeReasoning}, "finish_reason": "stop"}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer server.Close()
	client := groundingTestClient(t, server, false, nil)
	turns := []map[string]string{{"role": "user", "content": "synthetic"}}
	if answer, err := client.ChatGrounding(context.Background(), "JSON policy", turns, GroundingOptions{}); err != nil || answer != "{}" {
		t.Fatalf("bounded reasoning envelope failed: answer=%q err=%v", answer, err)
	}
	if _, err := client.Chat(context.Background(), "normal policy", turns); err == nil {
		t.Fatal("normal response resource limit changed")
	} else if d, ok := ErrorDiagnostics(err); !ok || d.Code != "model_response_too_large" {
		t.Fatalf("wrong normal limit error: %v", err)
	}
}

func TestGroundingEnvelopeHasAnExplicitResourceBoundary(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", (16<<20)+1)))
	}))
	defer server.Close()
	client := groundingTestClient(t, server, false, nil)
	answer, err := client.ChatGrounding(context.Background(), "JSON policy", []map[string]string{{"role": "user", "content": "synthetic"}}, GroundingOptions{})
	d, ok := ErrorDiagnostics(err)
	if !ok || d.Code != "model_response_too_large" || d.Attempts != 1 || answer != "" {
		t.Fatalf("review envelope boundary was not enforced: answer=%q err=%v", answer, err)
	}
}
