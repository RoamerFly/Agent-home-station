package linuxnative

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIncomingMediaReportsTerminalNativeFailures(t *testing.T) {
	tests := []struct{ state, reason, code string }{
		{"failed", "media_processing_failed", "incoming_media_native_processing_failed"},
		{"expired", "", "incoming_media_expired"},
		{"unsupported", "", "incoming_media_unsupported"},
		{"failed", "https://private.example/synthetic", "incoming_media_native_download_failed"},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/accounts/test/messages/message-one/media-transfers":
					write(w, `{"id":"download-one","state":"running"}`)
				case "/v1/accounts/test/jobs/download-one":
					write(w, fmt.Sprintf(`{"id":"download-one","state":%q,"reason":%q,"completedBytes":1376720,"totalBytes":1376720}`, test.state, test.reason))
				default:
					t.Errorf("tried to read content for a terminal download: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			data, _, err := testSender(t, server.URL).IncomingMedia(t.Context(), "message-one", 10)
			var failure *IncomingMediaTransferError
			if len(data) != 0 || !errors.As(err, &failure) || failure.Error() != test.code {
				t.Fatalf("terminal download: %q %v", data, err)
			}
			_, message := failure.SafeToolFailure()
			if strings.Contains(message, "private.example") || (strings.Contains(message, "1376720") != (test.reason == "media_processing_failed")) {
				t.Fatalf("unexpected safe diagnostic: %s", message)
			}
		})
	}
}

func TestIncomingVideoTriesDeclaredFullRenditionAfterTerminalFailure(t *testing.T) {
	verified := []byte("synthetic-normal-video")
	var submitted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/v1/accounts/test/") {
		case "messages/message-one/media":
			write(w, `{"items":[{"accountId":"test","messageId":"message-one","kind":"video","variant":"original","state":"pending"},{"accountId":"test","messageId":"message-one","kind":"video","variant":"normal","state":"pending"},{"accountId":"test","messageId":"message-one","kind":"video","variant":"thumbnail","actualVariant":"thumbnail","state":"available"}]}`)
		case "messages/message-one/media-transfers":
			var input struct{ Quality string }
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			submitted = append(submitted, input.Quality)
			switch input.Quality {
			case "original":
				write(w, `{"id":"job-original","accountId":"test","kind":"download","state":"failed","reason":"media_processing_failed"}`)
			case "normal":
				write(w, `{"id":"job-normal","accountId":"test","kind":"download","state":"available","assetId":"asset-normal"}`)
			default:
				t.Errorf("attempted a non-video rendition: %q", input.Quality)
				w.WriteHeader(http.StatusBadRequest)
			}
		case "media/asset-normal":
			write(w, fmt.Sprintf(`{"id":"asset-normal","accountId":"test","messageId":"message-one","kind":"video","state":"available","variant":"normal","actualVariant":"normal","byteLength":%d,"contentType":"video/mp4","sha256":"%x"}`, len(verified), sha256.Sum256(verified)))
		case "media/asset-normal/content":
			_, _ = w.Write(verified)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	var output bytes.Buffer
	result, err := testSender(t, server.URL).StreamVerifiedIncomingMedia(t.Context(), "message-one", "video", 100, &output)
	server.Close()
	if err != nil || result.Variant != "normal" || !bytes.Equal(output.Bytes(), verified) || strings.Join(submitted, ",") != "original,normal" {
		t.Fatalf("read %q, result %+v, requests %v, error %v", output.Bytes(), result, submitted, err)
	}
}

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
