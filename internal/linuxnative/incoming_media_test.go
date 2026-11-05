package linuxnative

import (
	"context"
	"encoding/json"
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
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["quality"] != "best" {
				t.Fatal("ordinary incoming media lost best transfer", body)
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

func TestIncomingVoiceMediaRequestsNormalTransfer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/media-transfers"):
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["quality"] != "normal" {
				t.Fatal("voice normal transfer missing", body)
			}
			write(w, `{"id":"voice-download","state":"available","assetId":"voice-asset"}`)
		case strings.HasSuffix(r.URL.Path, "/media/voice-asset/content"):
			w.Header().Set("Content-Type", "audio/ogg")
			_, _ = w.Write([]byte("normal-voice"))
		default:
			t.Fatal("unexpected path", r.URL.Path)
		}
	}))
	defer server.Close()
	data, typ, err := testSender(t, server.URL).IncomingVoiceMedia(context.Background(), "voice-message", 100)
	if err != nil || string(data) != "normal-voice" || typ != "audio/ogg" {
		t.Fatal(string(data), typ, err)
	}
}
