package native

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/sending"
)

func syntheticSession() SessionFile {
	ticket := binary.BigEndian.AppendUint32(nil, 1)
	ticket = append(ticket, 0)
	return SessionFile{Profile: profile, Experimental: true, ShortHost: "short.weixin.qq.com", ClientVersion: 0x28004039, UIN: 12345, SessionKey: bytes.Repeat([]byte{1}, 16), Cookie: bytes.Repeat([]byte{2}, 15), LoginSalt: []byte("synthetic-salt"), MMTLSPSK: bytes.Repeat([]byte{3}, 32), MMTLSTicket: ticket}
}
func writeSession(t *testing.T, s SessionFile) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.json")
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSessionlessEngineNeverSends(t *testing.T) {
	e, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	if e.Ready() {
		t.Fatal("sessionless engine was ready")
	}
	if _, err = e.SendText(context.Background(), sending.TextRequest{Recipient: "filehelper", Text: "hello"}); !errors.Is(err, sending.ErrNotReady) {
		t.Fatal(err)
	}
}
func TestSessionValidationDoesNotDial(t *testing.T) {
	e, err := New(writeSession(t, syntheticSession()))
	if err != nil {
		t.Fatal(err)
	}
	if !e.Ready() {
		t.Fatal("configured experimental engine not ready")
	}
	for name, change := range map[string]func(*SessionFile){
		"profile": func(s *SessionFile) { s.Profile = "unknown" }, "opt-in": func(s *SessionFile) { s.Experimental = false },
		"host": func(s *SessionFile) { s.ShortHost = "example.com" }, "userinfo": func(s *SessionFile) { s.ShortHost = "user@short.weixin.qq.com" },
		"key": func(s *SessionFile) { s.SessionKey = nil }, "cookie": func(s *SessionFile) { s.Cookie = nil }, "psk": func(s *SessionFile) { s.MMTLSPSK = nil },
	} {
		t.Run(name, func(t *testing.T) {
			s := syntheticSession()
			change(&s)
			if _, err := New(writeSession(t, s)); err == nil {
				t.Fatal("accepted invalid session")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAttemptIsEncryptedAndNeverRetriedOrFalselyAccepted(t *testing.T) {
	e, err := New(writeSession(t, syntheticSession()))
	if err != nil {
		t.Fatal(err)
	}
	s := e.(*sender)
	calls := 0
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "short.weixin.qq.com" || !strings.HasPrefix(r.URL.Path, "/mmtls/") || r.Method != "POST" {
			t.Fatal("wrong target")
		}
		b, _ := io.ReadAll(r.Body)
		if bytes.Contains(b, []byte("sensitive message")) {
			t.Fatal("plaintext message in outer request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("not a valid authenticated response")), Header: make(http.Header)}, nil
	})}
	_, err = s.SendText(context.Background(), sending.TextRequest{Recipient: "filehelper", Text: "sensitive message"})
	if !errors.Is(err, sending.ErrOutcomeUnknown) || calls != 1 {
		t.Fatalf("expected unknown with one attempt; %v calls=%d", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.SendText(ctx, sending.TextRequest{Recipient: "filehelper", Text: "hello"})
	if err == nil || calls != 1 {
		t.Fatal("cancelled request transmitted")
	}
}
