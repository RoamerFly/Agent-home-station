package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareWritesNewWireFileWithoutSending(t *testing.T) {
	out := filepath.Join(t.TempDir(), "request.pb")
	var stdout bytes.Buffer
	if err := prepare([]string{"-to", "room@chatroom", "-text", "hello", "-out", out}, &stdout); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(out)
	if err != nil || len(payload) == 0 {
		t.Fatalf("prepared file missing or empty: %v", err)
	}
	if !strings.Contains(stdout.String(), "no message was sent") {
		t.Fatalf("preparation output claims sending: %q", stdout.String())
	}
	if err := prepare([]string{"-to", "room@chatroom", "-text", "hello", "-out", out}, &stdout); err == nil {
		t.Fatal("prepare overwrote an existing request")
	}
}

func TestSendRequiresCompleteAcceptanceReceipt(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"empty", "", "outcome is unknown"},
		{"missing-id", `{"status":"accepted","recipient":"room@chatroom","client_message_id":"1"}`, "outcome is unknown"},
		{"wrong-recipient", `{"status":"accepted","recipient":"other","client_message_id":"1","server_message_id":"2"}`, "outcome is unknown"},
		{"zero-id", `{"status":"accepted","recipient":"room@chatroom","client_message_id":"0","server_message_id":"2"}`, "outcome is unknown"},
		{"not-decimal-id", `{"status":"accepted","recipient":"room@chatroom","client_message_id":"1.2","server_message_id":"2"}`, "outcome is unknown"},
		{"valid", `{"status":"accepted","recipient":"room@chatroom","client_message_id":"18446744073709551615","server_message_id":"2"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/messages/text" || r.Header.Get("Authorization") != "Bearer "+testToken {
					t.Errorf("unexpected local API request")
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			t.Setenv("WECHAT_API_TOKEN", testToken)
			t.Setenv("WECHAT_SENDER_URL", server.URL)
			var stdout bytes.Buffer
			err := send([]string{"-to", "room@chatroom", "-text", "hello"}, &stdout)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(stdout.String(), "delivery is not asserted") {
					t.Fatalf("send output overclaims delivery: %q", stdout.String())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSendGatewayTimeoutIsUnknownAndDoesNotRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte(`{"error":"outcome_unknown"}`))
	}))
	defer server.Close()
	t.Setenv("WECHAT_API_TOKEN", testToken)
	t.Setenv("WECHAT_SENDER_URL", server.URL)
	err := send([]string{"-to", "room@chatroom", "-text", "hello"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "outcome") || !strings.Contains(err.Error(), "do not retry automatically") {
		t.Fatalf("error = %v", err)
	}
}

func TestSenderEndpoint(t *testing.T) {
	got, err := senderEndpoint("http://127.0.0.1:8787/base/?leak=yes#fragment")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://127.0.0.1:8787/base/v1/messages/text" {
		t.Fatalf("endpoint = %q", got)
	}
	for _, raw := range []string{"", "ftp://localhost", "http:///missing-host", "http://token@localhost:8787"} {
		if _, err := senderEndpoint(raw); err == nil {
			t.Fatalf("accepted invalid sender URL %q", raw)
		}
	}
}

const testToken = "0123456789abcdef0123456789abcdef"
