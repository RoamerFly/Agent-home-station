package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestExternalMediaFetchUsesDeclaredOriginWithoutCredentialsOrRedirects(t *testing.T) {
	var allowedCalls, deniedCalls atomic.Int32
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deniedCalls.Add(1)
		_, _ = w.Write([]byte("unapproved"))
	}))
	defer denied.Close()
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowedCalls.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("provider credential forwarded to media URL")
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, denied.URL+"/secret", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("verified media"))
	}))
	defer allowed.Close()
	tool := APITool{Name: "result", URL: allowed.URL + "/create", Method: "GET", MediaKind: "file", MediaURLPath: "media_url",
		Headers:     map[string]string{"Authorization": "Bearer {{secret.key}}"},
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}
	bridge, err := NewExternalBridge([]Installed{{Manifest: Manifest{ID: "fixture", Name: "Fixture", Version: "1", Tools: []APITool{tool}},
		Enabled: true, Secrets: map[string]string{"key": "secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	media, err := bridge.FetchResultMedia(t.Context(), "fixture__result", allowed.URL+"/asset?ticket=private")
	if err != nil || string(media.Data) != "verified media" || allowedCalls.Load() != 1 {
		t.Fatal("declared media origin unavailable", err)
	}
	if _, err := bridge.FetchResultMedia(t.Context(), "fixture__result", denied.URL+"/secret"); err == nil || deniedCalls.Load() != 0 {
		t.Fatal("unapproved media origin contacted")
	}
	if _, err := bridge.FetchResultMedia(t.Context(), "fixture__result", allowed.URL+"/redirect"); err == nil || deniedCalls.Load() != 0 {
		t.Fatal("media redirect followed")
	}
	tool.MediaOrigins = []string{denied.URL}
	bridge, err = NewExternalBridge([]Installed{{Manifest: Manifest{ID: "fixture", Name: "Fixture", Version: "1", Tools: []APITool{tool}},
		Enabled: true, Secrets: map[string]string{"key": "secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.FetchResultMedia(context.Background(), "fixture__result", denied.URL+"/approved"); err != nil || deniedCalls.Load() != 1 {
		t.Fatal("explicitly approved media origin unavailable", err)
	}
}
