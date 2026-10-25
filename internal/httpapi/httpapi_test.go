package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/sending"
)

const testToken = "0123456789abcdef0123456789abcdef"

type fakeEngine struct {
	ready   bool
	receipt sending.Receipt
	err     error
	called  int
}

func (f *fakeEngine) Ready() bool { return f.ready }
func (f *fakeEngine) SendText(_ context.Context, _ sending.TextRequest) (sending.Receipt, error) {
	f.called++
	return f.receipt, f.err
}

func newTestHandler(t *testing.T, e *fakeEngine) http.Handler {
	t.Helper()
	h, err := New(Config{Token: testToken, AllowedRecipients: []string{"room@chatroom"}, SendTimeout: time.Second}, e)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func request(t *testing.T, h http.Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages/text", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func validBody() string { return `{"recipient":"room@chatroom","text":"hello"}` }

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	e := &fakeEngine{}
	for _, cfg := range []Config{
		{Token: "short", SendTimeout: time.Second},
		{Token: testToken, SendTimeout: 0},
		{Token: testToken, SendTimeout: time.Second, AllowedRecipients: []string{" "}},
	} {
		if _, err := New(cfg, e); err == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
	if _, err := New(Config{Token: testToken, SendTimeout: time.Second}, e); err != nil {
		t.Fatalf("an empty allowlist is valid and denies all: %v", err)
	}
}

func TestRejectsUnauthenticatedAndDisallowed(t *testing.T) {
	e := &fakeEngine{ready: true}
	h := newTestHandler(t, e)
	if got := request(t, h, "", validBody()).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", got)
	}
	if got := request(t, h, testToken, `{"recipient":"other","text":"hello"}`).Code; got != http.StatusForbidden {
		t.Fatalf("disallowed status = %d", got)
	}
	if e.called != 0 {
		t.Fatal("engine called for a rejected request")
	}
}

func TestStrictBodyValidation(t *testing.T) {
	e := &fakeEngine{ready: true}
	h := newTestHandler(t, e)
	for name, body := range map[string]string{
		"unknown-field": `{"recipient":"room@chatroom","text":"x","extra":true}`,
		"multiple-json": validBody() + `{}`,
		"array":         `[]`,
		"blank":         `{"recipient":"room@chatroom","text":"  "}`,
		"too-large":     `{"recipient":"room@chatroom","text":"` + strings.Repeat("x", maxTextBytes+1) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := request(t, h, testToken, body).Code; got != http.StatusBadRequest && got != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d", got)
			}
		})
	}
	invalid := []byte(`{"recipient":"room@chatroom","text":"`)
	invalid = append(invalid, 0xff)
	invalid = append(invalid, []byte(`"}`)...)
	r := httptest.NewRequest(http.MethodPost, "/v1/messages/text", strings.NewReader(string(invalid)))
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid UTF-8 status = %d", w.Code)
	}
}

func TestOversizeBodyAndNotReady(t *testing.T) {
	e := &fakeEngine{}
	h := newTestHandler(t, e)
	if got := request(t, h, testToken, `{"recipient":"room@chatroom","text":"`+strings.Repeat("x", maxBodyBytes)+`"}`).Code; got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status = %d", got)
	}
	if got := request(t, h, testToken, validBody()).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("not-ready status = %d", got)
	}
}

func TestSendOutcomesAndAcknowledgementValidation(t *testing.T) {
	cases := []struct {
		name    string
		receipt sending.Receipt
		err     error
		want    int
	}{
		{"unknown", sending.Receipt{}, sending.ErrOutcomeUnknown, http.StatusGatewayTimeout},
		{"rejected", sending.Receipt{}, sending.ErrRejected, http.StatusUnprocessableEntity},
		{"invalid-receipt", sending.Receipt{Recipient: "room@chatroom"}, nil, http.StatusBadGateway},
		{"wrong-recipient", sending.Receipt{Recipient: "other", ClientMessageID: "c", ServerMessageID: "s"}, nil, http.StatusBadGateway},
		{"accepted", sending.Receipt{Recipient: "room@chatroom", ClientMessageID: "c", ServerMessageID: "s"}, nil, http.StatusAccepted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &fakeEngine{ready: true, receipt: tc.receipt, err: tc.err}
			w := request(t, newTestHandler(t, e), testToken, validBody())
			if w.Code != tc.want {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMethodsHealthAndReadiness(t *testing.T) {
	e := &fakeEngine{ready: true}
	h := newTestHandler(t, e)
	for _, path := range []string{"/healthz", "/readyz"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || w.Header().Get("X-Request-ID") == "" {
			t.Fatalf("%s: status %d, request id %q", path, w.Code, w.Header().Get("X-Request-ID"))
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/messages/text", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d", w.Code)
	}
}

func TestTimeoutIsOutcomeUnknown(t *testing.T) {
	e := &fakeEngine{ready: true, err: errors.Join(sending.ErrOutcomeUnknown, context.DeadlineExceeded)}
	h, err := New(Config{Token: testToken, AllowedRecipients: []string{"room@chatroom"}, SendTimeout: time.Nanosecond}, e)
	if err != nil {
		t.Fatal(err)
	}
	if got := request(t, h, testToken, validBody()).Code; got != http.StatusGatewayTimeout {
		t.Fatalf("timeout status = %d", got)
	}
}
