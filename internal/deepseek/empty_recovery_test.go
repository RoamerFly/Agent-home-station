package deepseek

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmptyGenerationRetriesSameRequestOnceWithoutExecutingTools(t *testing.T) {
	for _, recover := range []bool{true, false} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			content := ""
			if recover && calls == 2 {
				content = `{\"action\":\"reply\",\"messages\":[\"好了\"]}`
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + content + `"},"finish_reason":"stop"}]}`))
		}))
		client := &Client{endpoint: server.URL, http: server.Client()}
		answer, e := client.request(context.Background(), []byte(`{"same_input":true}`))
		server.Close()
		if calls != 2 || recover && (e != nil || answer == "") || !recover && e == nil {
			t.Fatal("empty generation recovery unbounded", recover, calls, answer, e)
		}
	}
}
