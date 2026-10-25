package linuxnative

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIncomingMediaUsesOwnedTransferAndBoundedContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatal("missing local native authorization")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/media-transfers"):
			if r.Method != "POST" {
				t.Fatal("invalid transfer method")
			}
			write(w, `{"id":"download-one","state":"available","assetId":"asset-one"}`)
		case strings.HasSuffix(r.URL.Path, "/media/asset-one/content"):
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("image-bytes"))
		default:
			t.Fatal("unexpected path", r.URL.Path)
		}
	}))
	defer server.Close()
	s := testSender(t, server.URL)
	data, typ, err := s.IncomingMedia(context.Background(), "message-one", 100)
	if err != nil || string(data) != "image-bytes" || typ != "image/png" {
		t.Fatal(string(data), typ, err)
	}
	if _, _, err = s.IncomingMedia(context.Background(), "message-one", 2); err == nil {
		t.Fatal("media limit ignored")
	}
}
