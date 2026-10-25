package linuxnative

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"wechat-bot/internal/sending"
)

func TestQueuedThenAcceptedReturnsExactReceipt(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/accounts/test":
			write(w, `{"authenticated":true}`)
		case "/v1/accounts/test/sends":
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Idempotency-Key") == "" {
				t.Fatalf("bad create request")
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"send-1","destination":"filehelper","state":"queued"}`))
		case "/v1/accounts/test/sends/send-1":
			if polls.Add(1) == 1 {
				write(w, `{"id":"send-1","destination":"filehelper","state":"submitting"}`)
				return
			}
			write(w, `{"id":"send-1","destination":"filehelper","state":"accepted","clientId":"42","serverId":"9001"}`)
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()

	sender := testSender(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	receipt, err := sender.SendText(ctx, sending.TextRequest{Recipient: "filehelper", Text: "后台发送测试"})
	if err != nil {
		t.Fatal(err)
	}
	if receipt != (sending.Receipt{Recipient: "filehelper", ClientMessageID: "42", ServerMessageID: "9001"}) || polls.Load() != 2 {
		t.Fatalf("receipt=%+v polls=%d", receipt, polls.Load())
	}
}

func TestUnsafeAcceptedOutcomeIsUnknown(t *testing.T) {
	for _, intent := range []string{
		`{"id":"one","destination":"other","state":"accepted","clientId":"1","serverId":"2"}`,
		`{"id":"one","destination":"filehelper","state":"accepted","clientId":"0","serverId":"2"}`,
		`{"id":"one","destination":"filehelper","state":"accepted","clientId":1,"serverId":"2"}`,
		`{"id":"one","destination":"filehelper","state":"mystery","clientId":"1","serverId":"2"}`,
	} {
		t.Run(intent, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/accounts/test" {
					write(w, `{"authenticated":true}`)
					return
				}
				if r.URL.Path == "/v1/accounts/test/sends" {
					w.WriteHeader(http.StatusAccepted)
					_, _ = w.Write([]byte(intent))
					return
				}
				t.Fatalf("unexpected poll")
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := testSender(t, server.URL).SendText(ctx, sending.TextRequest{Recipient: "filehelper", Text: "x"})
			if !errors.Is(err, sending.ErrOutcomeUnknown) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestNotAuthenticatedDoesNotPost(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			t.Fatal("POST must not occur")
		}
		write(w, `{"authenticated":false}`)
	}))
	defer server.Close()
	_, err := testSender(t, server.URL).SendText(context.Background(), sending.TextRequest{Recipient: "filehelper", Text: "x"})
	if !errors.Is(err, sending.ErrNotReady) || posts.Load() != 0 {
		t.Fatalf("error=%v posts=%d", err, posts.Load())
	}
}

func TestPostConnectionLossIsNotRetried(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/accounts/test" {
			write(w, `{"authenticated":true}`)
			return
		}
		if r.Method == http.MethodPost {
			posts.Add(1)
			panic(http.ErrAbortHandler)
		}
		t.Fatalf("unexpected route")
	}))
	defer server.Close()
	_, err := testSender(t, server.URL).SendText(context.Background(), sending.TextRequest{Recipient: "filehelper", Text: "x"})
	if !errors.Is(err, sending.ErrOutcomeUnknown) || posts.Load() != 1 {
		t.Fatalf("error=%v posts=%d", err, posts.Load())
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var destinationHits atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationHits.Add(1) }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer source.Close()
	if testSender(t, source.URL).Ready() || destinationHits.Load() != 0 {
		t.Fatalf("redirect followed: %d", destinationHits.Load())
	}
}

func testSender(t *testing.T, endpoint string) *Sender {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sender, err := New(Config{URL: endpoint, TokenFile: tokenFile, Account: "test", PollInterval: time.Millisecond, ReadyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return sender
}

func TestNeedsReviewPreservesNativeDiagnosticAndNeverResends(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/accounts/test" {
			write(w, `{"authenticated":true}`)
			return
		}
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(202)
			write(w, `{"id":"uncertain-send","destination":"filehelper","state":"needs_review","errorCode":"submission_outcome_unknown"}`)
			return
		}
		t.Fatal("terminal state must not poll or retry")
	}))
	defer server.Close()
	_, err := testSender(t, server.URL).SendText(context.Background(), sending.TextRequest{Recipient: "filehelper", Text: "x"})
	if !errors.Is(err, sending.ErrOutcomeUnknown) || sending.FailureReason(err) != "submission_outcome_unknown" || sending.IntentID(err) != "uncertain-send" || posts.Load() != 1 {
		t.Fatal("lost native evidence", err, posts.Load())
	}
}

func write(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}
