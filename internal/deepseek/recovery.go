package deepseek

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Diagnostics contains fixed codes and numbers only. Upstream bodies, URLs,
// headers, prompts, keys and underlying error strings never enter this type.
type Diagnostics struct {
	Code         string
	Stage        string
	HTTPStatus   int
	Attempts     int
	MaxAttempts  int
	DurationMS   int64
	RetryDelayMS int64
	Retryable    bool
}

type RequestError struct {
	Diagnostics
	cause      error
	retryAfter time.Duration
}

func (e *RequestError) Error() string { return e.Code }
func (e *RequestError) Unwrap() error { return e.cause }

func ErrorDiagnostics(err error) (Diagnostics, bool) {
	var failure *RequestError
	if errors.As(err, &failure) {
		return failure.Diagnostics, true
	}
	return Diagnostics{}, false
}

// UserMessage is suitable for an operator-facing test failure. No raw provider
// text or underlying transport error is included.
func UserMessage(err error) string {
	d, ok := ErrorDiagnostics(err)
	if !ok {
		return "模型调用失败，请检查服务商配置"
	}
	messages := map[string]string{
		"model_output_token_limit": "模型输出达到Token上限，内容被截断", "model_content_filtered": "模型服务停止了本次内容生成", "model_tool_call_count_invalid": "模型返回了不受支持的工具调用数量", "model_tool_arguments_too_large": "模型工具参数超过长度限制", "model_tool_arguments_invalid": "模型返回的工具参数格式无效",
		"model_network_unavailable": "模型网络连接不可用", "model_network_timeout": "模型连接或响应超时", "model_connection_closed": "模型连接提前关闭",
		"model_dns_failed": "模型服务域名解析失败", "model_tls_validation_failed": "模型服务证书验证失败", "model_authentication_failed": "模型鉴权失败，请检查 Key",
		"model_insufficient_balance": "模型账户额度不足", "model_invalid_parameters": "模型请求参数不受支持", "model_endpoint_or_model_missing": "服务地址或模型不存在",
		"model_rate_limited": "模型服务限流", "model_service_unavailable": "模型服务暂时不可用", "model_upstream_timeout": "模型服务处理超时",
		"model_redirect_denied": "模型地址返回跳转，未转发凭据", "model_request_cancelled": "模型请求已取消", "model_deadline_exceeded": "本次模型处理超时",
		"model_response_too_large": "模型响应超过大小限制", "model_invalid_response": "模型返回格式无效", "model_response_incomplete": "模型响应未完整生成", "model_response_empty": "模型返回空内容",
	}
	message := messages[d.Code]
	if message == "" {
		message = "模型调用失败"
	}
	if d.HTTPStatus > 0 {
		message += fmt.Sprintf("（HTTP %d）", d.HTTPStatus)
	}
	if d.Attempts > 0 {
		message += fmt.Sprintf("；已尝试 %d 次", d.Attempts)
	}
	return message
}

type requestObserverKey struct{}

// WithRequestObserver scopes retry/recovery events to one conversation. The
// callback is not stored on the shared client, so simultaneous previews cannot
// inherit another conversation's tracing metadata.
func WithRequestObserver(ctx context.Context, observer func(string, Diagnostics)) context.Context {
	return context.WithValue(ctx, requestObserverKey{}, observer)
}

func observeRequest(ctx context.Context, event string, d Diagnostics) {
	if observer, ok := ctx.Value(requestObserverKey{}).(func(string, Diagnostics)); ok && observer != nil {
		observer(event, d)
	}
}

func requestFailure(code, stage string, status int, retryable bool) *RequestError {
	return &RequestError{Diagnostics: Diagnostics{Code: code, Stage: stage, HTTPStatus: status, Retryable: retryable}}
}

func transportFailure(ctx context.Context, err error, stage string) *RequestError {
	if ctx.Err() != nil {
		code := "model_request_cancelled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = "model_deadline_exceeded"
		}
		failure := requestFailure(code, stage, 0, false)
		failure.cause = ctx.Err()
		return failure
	}
	var tlsError *tls.CertificateVerificationError
	var certError x509.UnknownAuthorityError
	if errors.As(err, &tlsError) || errors.As(err, &certError) {
		return requestFailure("model_tls_validation_failed", stage, 0, false)
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return requestFailure("model_dns_failed", stage, 0, !dns.IsNotFound)
	}
	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return requestFailure("model_network_timeout", stage, 0, true)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return requestFailure("model_connection_closed", stage, 0, true)
	}
	return requestFailure("model_network_unavailable", stage, 0, true)
}

func statusFailure(status int) *RequestError {
	code, retryable := "model_http_rejected", false
	switch status {
	case 400, 422:
		code = "model_invalid_parameters"
	case 401, 403:
		code = "model_authentication_failed"
	case 402:
		code = "model_insufficient_balance"
	case 404:
		code = "model_endpoint_or_model_missing"
	case 408, 504:
		code, retryable = "model_upstream_timeout", true
	case 429:
		code, retryable = "model_rate_limited", true
	case 500, 502, 503:
		code, retryable = "model_service_unavailable", true
	}
	return requestFailure(code, "http_status", status, retryable)
}

func retryAfter(raw string, now time.Time) time.Duration {
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(raw); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 0
}

func (c *Client) request(ctx context.Context, body []byte) (string, error) {
	start := time.Now()
	const attempts = 2
	for attempt := 1; attempt <= attempts; attempt++ {
		answer, failure := c.requestOnce(ctx, body)
		if failure == nil {
			if attempt > 1 {
				observeRequest(ctx, "model_recovered", Diagnostics{Attempts: attempt, MaxAttempts: attempts, DurationMS: time.Since(start).Milliseconds()})
			}
			return answer, nil
		}
		failure.Attempts, failure.MaxAttempts = attempt, attempts
		failure.DurationMS = time.Since(start).Milliseconds()
		// Generation requests have no external side effects. Only repeat the
		// same generation once; never repeat an executed tool or WeChat send.
		if !failure.Retryable || attempt == attempts || ctx.Err() != nil {
			return "", failure
		}
		delay := time.Duration(750+rand.IntN(501)) * time.Millisecond
		if failure.retryAfter > delay {
			delay = failure.retryAfter
		}
		// Respect a long Retry-After without tying up the conversation worker.
		if delay > 30*time.Second {
			return "", failure
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
			return "", failure
		}
		failure.RetryDelayMS = delay.Milliseconds()
		observeRequest(ctx, "model_retry", failure.Diagnostics)
		wait := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			wait.Stop()
			cancelled := transportFailure(ctx, ctx.Err(), "retry_wait")
			cancelled.Attempts, cancelled.MaxAttempts, cancelled.DurationMS = attempt, attempts, time.Since(start).Milliseconds()
			return "", cancelled
		case <-wait.C:
		}
	}
	panic("unreachable bounded request loop")
}
