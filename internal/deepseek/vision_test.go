package deepseek

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVisionUsesUserImageBlocksAndNeverRetries(t *testing.T) {
	data, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=")
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var v map[string]any
		if json.NewDecoder(r.Body).Decode(&v) != nil {
			t.Fatal("bad JSON")
		}
		turns := v["messages"].([]any)
		parts := turns[1].(map[string]any)["content"].([]any)
		if len(parts) != 2 || parts[1].(map[string]any)["type"] != "image_url" {
			t.Fatal("image input missing")
		}
		if turns[1].(map[string]any)["role"] != "user" {
			t.Fatal("image in wrong role")
		}
		w.WriteHeader(400)
	}))
	defer s.Close()
	c := &Client{endpoint: s.URL, key: "private-test-key", model: "deepseek-flash", maxTokens: 768, http: s.Client()}
	if _, err := c.Vision(context.Background(), "describe", []Image{{Data: data}}); err == nil || calls != 1 {
		t.Fatal("unexpected retry or hidden failure", err, calls)
	}
}
