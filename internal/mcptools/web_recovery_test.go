package mcptools

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestPublicGETRecoveryIsBoundedAndReportsSafeStages(t *testing.T) {
	for _, failure := range []error{io.ErrUnexpectedEOF, &WebError{Code: "http_status", Status: 503, Stage: "http_status"}} {
		calls, waits := 0, 0
		events := []WebAttempt{}
		ctx := WithWebObserver(context.Background(), func(d WebAttempt) { events = append(events, d) })
		b, _, _, _, err := recoverPublicRead(ctx, func(context.Context) ([]byte, string, string, rangeHeaders, error) {
			calls++
			if calls == 1 {
				return nil, "", "", rangeHeaders{}, failure
			}
			return []byte("complete"), "text/plain", "https://example.org", rangeHeaders{Status: 200}, nil
		}, func(context.Context, time.Duration) error { waits++; return nil })
		if err != nil || string(b) != "complete" || calls != 2 || waits != 1 || len(events) != 2 || events[1].Attempt != 2 {
			t.Fatal(calls, waits, events, err)
		}
	}
	for _, failure := range []error{&WebError{Code: "private_destination"}, &WebError{Code: "http_status", Status: 403}, &WebError{Code: "http_status", Status: 429, RetryAfter: time.Minute}, x509.UnknownAuthorityError{}} {
		calls := 0
		_, _, _, _, err := recoverPublicRead(context.Background(), func(context.Context) ([]byte, string, string, rangeHeaders, error) {
			calls++
			return nil, "", "", rangeHeaders{}, failure
		}, func(context.Context, time.Duration) error { t.Fatal("permanent failure retried"); return nil })
		if err == nil || calls != 1 {
			t.Fatal(calls, err)
		}
	}
	calls := 0
	_, _, _, _, err := recoverPublicRead(context.Background(), func(context.Context) ([]byte, string, string, rangeHeaders, error) {
		calls++
		return nil, "", "", rangeHeaders{}, errors.New("https://private.invalid/?key=secret")
	}, func(context.Context, time.Duration) error { return nil })
	if calls != 2 || err == nil || strings.Contains(FailureDescription(err), "secret") || strings.Contains(FailureDescription(err), "private.invalid") || !strings.Contains(FailureDescription(err), "尝试2次") {
		t.Fatal(calls, FailureDescription(err))
	}
}

func TestPublicGETRetryWaitCancellationDoesNotHang(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, _, _, _, err := recoverPublicRead(ctx, func(context.Context) ([]byte, string, string, rangeHeaders, error) {
		calls++
		return nil, "", "", rangeHeaders{}, io.EOF
	}, func(context.Context, time.Duration) error { cancel(); return context.Canceled })
	var web *WebError
	if calls != 1 || !errors.As(err, &web) || web.Code != "request_cancelled" || web.Stage != "retry_wait" {
		t.Fatal(calls, err)
	}
}
