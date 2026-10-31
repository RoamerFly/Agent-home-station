package mcptools

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func syntheticPublicTransport(t *testing.T, handler http.HandlerFunc) context.Context {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return context.WithValue(t.Context(), publicSessionKey{}, &publicReadSession{Transport: transport})
}

func TestPublicPluginMediaSourcePrivacyAndRedirect(t *testing.T) {
	var foreign atomic.Int32
	ctx := syntheticPublicTransport(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "foreign.example" {
			foreign.Add(1)
		}
		if r.Header.Get("Referer") != "https://source.example/" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("source credentials leaked or origin lost", r.Header)
		}
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "http://cdn.example/media", http.StatusFound)
		case "/foreign":
			http.Redirect(w, r, "http://foreign.example/media", http.StatusFound)
		default:
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p'})
		}
	})
	for _, path := range []string{"same", "foreign"} {
		raw := "http://cdn.example/" + path
		scoped, err := publicPluginMediaContext(ctx, raw, "https://source.example/private/path?token=synthetic")
		if err != nil {
			t.Fatal(err)
		}
		b, _, _, _, err := publicReadRangeOnce(scoped, raw, 1024, 2*time.Second, "", "")
		if path == "same" && (err != nil || len(b) == 0) {
			t.Fatal("same-origin media", err)
		}
		if path == "foreign" && err == nil {
			t.Fatal("cross-origin redirect accepted")
		}
	}
	if foreign.Load() != 0 {
		t.Fatal("foreign host contacted")
	}
	credentialURL := (&url.URL{Scheme: "https", Host: "cdn.example", Path: "/video", User: url.UserPassword("synthetic", "synthetic")}).String()
	for _, raw := range []string{"file:///private", "http://127.0.0.1/video", credentialURL} {
		if _, err := publicPluginMediaContext(ctx, raw, "https://source.example/"); err == nil {
			t.Fatal("unsafe locator accepted", raw)
		}
	}
}

func TestPublicPluginShareSourceDoesNotDownloadLargeBinary(t *testing.T) {
	ctx := syntheticPublicTransport(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media" {
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", "10000000")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(strings.Repeat("page", 200)))
	})
	b, kind, final, err := ReadPublicPluginShareSource(ctx, "http://cdn.example/media", 512, time.Second)
	if err != nil || len(b) != 0 || kind != "video/mp4" || final != "http://cdn.example/media" {
		t.Fatal("binary was downloaded in parser", len(b), kind, err)
	}
	if _, _, _, err = ReadPublicPluginShareSource(ctx, "http://cdn.example/page", 512, time.Second); err == nil {
		t.Fatal("page size bound bypassed")
	}
	if _, _, _, err = ReadPublicPluginPage(ctx, "http://cdn.example/page", 512, time.Second); err == nil {
		t.Fatal("ordinary reader size bound bypassed")
	}
}
