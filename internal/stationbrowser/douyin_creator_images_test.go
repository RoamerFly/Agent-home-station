package stationbrowser

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCreatorImageDownloadChecksRedirectMIMEAndSize(t *testing.T) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 3, 4))); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("revoked fixture grant")
	var revoked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("image request transmitted a method or credential outside its scope")
		}
		switch r.URL.Path {
		case "/image":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngData.Bytes())
		case "/redirect":
			http.Redirect(w, r, "/never", http.StatusFound)
		case "/wrong-type":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(pngData.Bytes())
		case "/large":
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Length", "2097153")
		case "/revoke":
			revoked.Store(true)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngData.Bytes())
		default:
			t.Error("blocked redirect destination received a request")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	authorize := func() error {
		if revoked.Load() {
			return denied
		}
		return nil
	}
	allow := func(raw string) bool { return strings.HasPrefix(raw, server.URL+"/") && raw != server.URL+"/never" }
	fetch := func(path string) (DouyinWebMedia, error) {
		return fetchDouyinCreatorImage(t.Context(), server.URL+path, server.Client(), authorize, allow)
	}
	if got, err := fetch("/image"); err != nil || got.ContentType != "image/png" || !bytes.Equal(got.Data, pngData.Bytes()) {
		t.Fatalf("expected exact PNG: %s %v", got.ContentType, err)
	}
	for _, path := range []string{"/redirect", "/wrong-type", "/large"} {
		if got, err := fetch(path); err == nil || got.Data != nil {
			t.Errorf("%s yielded data: %d %v", path, len(got.Data), err)
		}
	}
	if got, err := fetch("/revoke"); !errors.Is(err, denied) || got.Data != nil {
		t.Fatalf("revocation yielded data: %d %v", len(got.Data), err)
	}
	for _, raw := range []string{server.URL + "/image", "http://cdn.douyinpic.com/image", "https://cdn.douyinpic.com.attacker.test/image", "https://cdn.douyinpic.com:8443/image", "https://user@cdn.douyinpic.com/image"} {
		if validDouyinCreatorImageURL(raw) {
			t.Errorf("accepted disallowed image URL: %s", raw)
		}
	}
}
