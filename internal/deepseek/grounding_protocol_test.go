package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type syntheticReviewProtocolError struct{}

func (syntheticReviewProtocolError) Error() string {
	return "PRIVATE provider output must not be copied"
}
func (syntheticReviewProtocolError) ReviewProtocolCode() string { return "review_units_missing" }

func TestGroundingProtocolRepairSharesFormatBudgetAndPreservesVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name, first, second string
		wantCalls           int
		wantCode            string
	}{
		{"schema_recovered", `{}`, `{"units":[]}`, 2, ""},
		{"schema_still_invalid", `{}`, `{}`, 2, "model_review_protocol_invalid"},
		{"syntax_then_schema", `broken`, `{}`, 2, "model_review_protocol_invalid"},
		{"schema_then_syntax", `{}`, `broken`, 2, "model_invalid_response"},
		{"semantic_reject", `{"units":[],"verdict":"reject"}`, `unused`, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			var requests []map[string]any
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				json.NewDecoder(r.Body).Decode(&request)
				requests = append(requests, request)
				content := tc.first
				if calls.Add(1) > 1 {
					content = tc.second
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": "stop"}}})
			}))
			defer server.Close()
			client := groundingTestClient(t, server, false, nil)
			var events []string
			ctx := WithRequestObserver(t.Context(), func(event string, _ Diagnostics) { events = append(events, event) })
			answer, err := client.ChatGroundingValidated(ctx, "synthetic JSON review", []map[string]string{{"role": "user", "content": "fixed candidate and evidence"}}, GroundingOptions{}, func(answer string) error {
				if !strings.Contains(answer, `"units"`) {
					return syntheticReviewProtocolError{}
				}
				return nil
			})
			if int(calls.Load()) != tc.wantCalls {
				t.Fatalf("calls=%d", calls.Load())
			}
			if tc.wantCode == "" {
				if err != nil || answer == "" {
					t.Fatalf("answer=%q err=%v", answer, err)
				}
			} else {
				d, ok := ErrorDiagnostics(err)
				if !ok || d.Code != tc.wantCode || d.FormatAttempt != 2 || answer != "" {
					t.Fatalf("err=%v diag=%+v", err, d)
				}
				if tc.wantCode == "model_review_protocol_invalid" {
					var protocol syntheticReviewProtocolError
					if d.ProtocolCode != "review_units_missing" || !errors.As(err, &protocol) {
						t.Fatalf("lost protocol diagnostic: %+v", d)
					}
				}
			}
			for _, request := range requests {
				messages := request["messages"].([]any)
				if len(messages) != 2 || messages[1].(map[string]any)["content"] != "fixed candidate and evidence" {
					t.Fatal("review input changed")
				}
				b, _ := json.Marshal(request)
				if strings.Contains(string(b), "PRIVATE") {
					t.Fatal("raw error leaked into retry")
				}
			}
			if tc.wantCalls == 2 && !strings.Contains(strings.Join(events, ","), "model_grounding_format_repair") {
				t.Fatal("repair was not observable")
			}
		})
	}
}

func TestGroundingProtocolRepairDoesNotRetryTransportFailures(t *testing.T) {
	for _, status := range []int{401, 402} {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(status) }))
		client := groundingTestClient(t, server, false, nil)
		_, err := client.ChatGroundingValidated(context.Background(), "JSON", []map[string]string{{"role": "user", "content": "synthetic"}}, GroundingOptions{}, func(string) error { t.Fatal("validator called on HTTP failure"); return nil })
		server.Close()
		if err == nil || calls.Load() != 1 {
			t.Fatalf("status=%d err=%v calls=%d", status, err, calls.Load())
		}
	}
}

func TestGroundingProtocolRepairDoesNotRetryChangedHostEvidence(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	client := groundingTestClient(t, server, false, nil)
	changed := errors.New("synthetic host evidence changed")
	_, err := client.ChatGroundingValidated(t.Context(), "JSON", []map[string]string{{"role": "user", "content": "synthetic"}}, GroundingOptions{}, func(string) error { return changed })
	if !errors.Is(err, changed) || calls.Load() != 1 {
		t.Fatalf("changed host state retried: %v calls=%d", err, calls.Load())
	}
}
