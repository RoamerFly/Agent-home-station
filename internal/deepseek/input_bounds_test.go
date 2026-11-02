package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStructuredReviewInputDoesNotInheritShortChatByteLimit(t *testing.T) {
	input := strings.Repeat("合成", 23000)
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Messages []map[string]string `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) != 2 || body.Messages[1]["content"] != input {
			t.Error("structured evidence was truncated or changed")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"units\":[]}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	c := groundingTestClient(t, server, true, nil)
	turns := []map[string]string{{"role": "user", "content": input}}
	if _, err := c.ChatGrounding(context.Background(), "synthetic review", turns, GroundingOptions{}); err != nil || calls != 1 {
		t.Fatal("review failed before HTTP", err, calls)
	}
	for _, review := range []bool{false, true} {
		turns[0]["content"] = input
		var err error
		if review {
			turns[0]["content"] = strings.Repeat("x", (1<<20)+1)
			_, err = c.ChatGrounding(context.Background(), "synthetic review", turns, GroundingOptions{})
		} else {
			_, err = c.Chat(context.Background(), "synthetic conversation", turns)
		}
		d, ok := ErrorDiagnostics(err)
		if !ok || d.Code != "model_input_transport_limit" || d.InputBytes <= d.InputByteLimit || calls != 1 {
			t.Fatal("input boundary lost safe diagnostics or performed HTTP", err, d, calls)
		}
	}
}
