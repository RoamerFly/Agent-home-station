package deepseek

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestModelCatalogueDoesNotGenerateOrExposeCredentials(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Error("wrong catalogue request")
		}
		w.Write([]byte(`{"data":[{"id":"model-z"},{"id":"deepseek-flash"},{"id":"model-z"}]}`))
	}))
	defer server.Close()
	c := &Client{endpoint: server.URL + "/v1/chat/completions", key: "synthetic-key", http: server.Client(), onUsage: func(Usage) { t.Error("catalogue generated tokens") }}
	ids, err := c.Models(context.Background())
	if err != nil || !reflect.DeepEqual(ids, []string{"deepseek-flash", "model-z"}) || calls != 1 {
		t.Fatal(ids, err, calls)
	}
}
