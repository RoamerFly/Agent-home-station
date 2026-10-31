package mcptools

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type pluginRouteTransport func(*http.Request) (*http.Response, error)

func (f pluginRouteTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPluginRouteDoesNotFollowCredentialRedirectOrRetry(t *testing.T) {
	for _, status := range []int{302, 401, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			tr := pluginRouteTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "https://provider.example/api/read" || r.Header.Get("Cookie") != "synthetic=private" || r.Method != "POST" {
					t.Fatal("request changed")
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://other.example/steal"}}, Body: io.NopCloser(strings.NewReader("private error body must not leak")), Request: r}, nil
			})
			_, err := postPluginRoute(context.Background(), tr, "https://provider.example/api/read", map[string]string{"Cookie": "synthetic=private"}, []byte(`{"id":"synthetic"}`), 100, time.Second)
			if err == nil || calls != 1 || strings.Contains(err.Error(), "private") {
				t.Fatal("redirect/retry/diagnostic leak", calls, err)
			}
		})
	}
}
func TestPluginRouteBoundsResponseAndHidesNetworkErrors(t *testing.T) {
	for _, body := range []string{`{"ok":true}`, "not-json", strings.Repeat(" ", 101)} {
		tr := pluginRouteTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})
		result, err := postPluginRoute(context.Background(), tr, "https://provider.example/api", nil, []byte(`{}`), 100, time.Second)
		if body == `{"ok":true}` {
			if err != nil || string(result) != body {
				t.Fatal(result, err)
			}
		} else if err == nil {
			t.Fatal("invalid/oversized response accepted")
		}
	}
	tr := pluginRouteTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret diagnostic") })
	_, err := postPluginRoute(context.Background(), tr, "https://provider.example/api", nil, []byte(`{}`), 100, time.Second)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("raw transport error escaped", err)
	}
	if _, err = PostPluginRoute(context.Background(), "http://127.0.0.1/api", nil, []byte(`{}`), 100, time.Second); err == nil {
		t.Fatal("non HTTPS route accepted")
	}
}
