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

func TestResourceReuseClassifierRequiresExplicitBoolean(t *testing.T) {
	for _, answer := range []string{`{"reuse":true}`, `{"reuse":false}`, `{}`, `{"reuse":"true"}`} {
		t.Run(answer, func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					MaxTokens int `json:"max_tokens"`
				}
				if json.NewDecoder(r.Body).Decode(&in) != nil || in.MaxTokens != 128 {
					t.Error("unbounded intent check")
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": answer}, "finish_reason": "stop"}}})
			}))
			defer s.Close()
			key := filepath.Join(t.TempDir(), "key")
			os.WriteFile(key, []byte("synthetic-test-key"), 0600)
			c, err := New(Config{BaseURL: s.URL, KeyFile: key})
			if err != nil {
				t.Fatal(err)
			}
			c.http = s.Client()
			reuse, err := c.VerifyResourceReuse(context.Background(), "重发刚才那首")
			valid := answer == `{"reuse":true}` || answer == `{"reuse":false}`
			if (err == nil) != valid || reuse != (answer == `{"reuse":true}`) {
				t.Fatal(reuse, err)
			}
		})
	}
}
