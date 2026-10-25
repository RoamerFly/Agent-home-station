package mcptools

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Only idempotent public GETs use recovery. MCP calls, plugin submissions,
// native uploads and WeChat sends are never replayed by this mechanism.
type WebAttempt struct {
	Code, Stage         string
	HTTPStatus, Attempt int
}
type webObserverKey struct{}

func WithWebObserver(ctx context.Context, fn func(WebAttempt)) context.Context {
	return context.WithValue(ctx, webObserverKey{}, fn)
}
func observeWeb(ctx context.Context, d WebAttempt) {
	if fn, ok := ctx.Value(webObserverKey{}).(func(WebAttempt)); ok {
		fn(d)
	}
}

func classifyWeb(err error, stage string) *WebError {
	var original *WebError
	if errors.As(err, &original) {
		copy := *original
		if copy.Stage == "" {
			copy.Stage = stage
		}
		return &copy
	}
	d := &WebError{Code: "network_unavailable", Stage: stage}
	var dns *net.DNSError
	var network net.Error
	var certificate *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	switch {
	case errors.Is(err, context.Canceled):
		d.Code = "request_cancelled"
	case errors.As(err, &certificate) || errors.As(err, &unknown) || errors.As(err, &host) || errors.As(err, &invalid):
		d.Code = "tls_failed"
	case errors.As(err, &dns):
		d.Code = "dns_failed"
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout():
		d.Code = "network_timeout"
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		d.Code = "network_closed"
	}
	return d
}
func retryableWeb(d *WebError) bool {
	if d.Code == "http_status" {
		switch d.Status {
		case 408, 429, 500, 502, 503, 504:
			return true
		}
		return false
	}
	return d.Code == "network_unavailable" || d.Code == "network_closed" || d.Code == "network_timeout" || d.Code == "dns_failed"
}
func retryAfter(value string) time.Duration {
	if n, err := strconv.Atoi(value); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		if delay := time.Until(t); delay > 0 {
			return delay
		}
	}
	return 0
}

func publicReadRange(ctx context.Context, raw string, maxBytes int64, timeout time.Duration, byteRange, validator string) ([]byte, string, string, rangeHeaders, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return recoverPublicRead(ctx, func(ctx context.Context) ([]byte, string, string, rangeHeaders, error) {
		return publicReadRangeOnce(ctx, raw, maxBytes, timeout, byteRange, validator)
	}, func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	})
}

type publicReader func(context.Context) ([]byte, string, string, rangeHeaders, error)

func recoverPublicRead(ctx context.Context, read publicReader, wait func(context.Context, time.Duration) error) ([]byte, string, string, rangeHeaders, error) {
	for attempt := 1; attempt <= 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, "", "", rangeHeaders{}, classifyWeb(err, "request")
		}
		b, t, u, h, err := read(ctx)
		if err == nil {
			observeWeb(ctx, WebAttempt{HTTPStatus: h.Status, Attempt: attempt})
			return b, t, u, h, nil
		}
		d := classifyWeb(err, "connection_or_headers")
		d.Attempts = attempt
		observeWeb(ctx, WebAttempt{Code: d.Code, Stage: d.Stage, HTTPStatus: d.Status, Attempt: attempt})
		if attempt == 2 || ctx.Err() != nil || !retryableWeb(d) || d.RetryAfter > 5*time.Second {
			return nil, "", "", rangeHeaders{}, d
		}
		delay := 500 * time.Millisecond
		if d.RetryAfter > delay {
			delay = d.RetryAfter
		}
		if err = wait(ctx, delay); err != nil {
			stopped := classifyWeb(err, "retry_wait")
			stopped.Attempts = attempt
			return nil, "", "", rangeHeaders{}, stopped
		}
	}
	panic("unreachable public GET recovery")
}
