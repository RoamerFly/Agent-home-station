package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PostPluginRoute sends exactly one host-approved JSON request. In particular,
// credentials never follow redirects and ambiguous requests are not retried.
// endpoint/headers must originate from an approved manifest, not model args.
func PostPluginRoute(ctx context.Context, endpoint string, headers map[string]string, body json.RawMessage, maxBytes int64, timeout time.Duration) (json.RawMessage, error) {
	tr := newPublicTransport()
	defer tr.CloseIdleConnections()
	return postPluginRoute(ctx, tr, endpoint, headers, body, maxBytes, timeout)
}
func postPluginRoute(ctx context.Context, tr http.RoundTripper, endpoint string, headers map[string]string, body json.RawMessage, maxBytes int64, timeout time.Duration) (json.RawMessage, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || validPublicURL(endpoint) != nil || u.RawQuery != "" || u.Opaque != "" || maxBytes < 1 || maxBytes > 4<<20 || timeout <= 0 || len(body) > 1<<20 || !json.Valid(body) {
		return nil, errors.New("invalid plugin route request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid plugin route request")
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		if key != "Cookie" && key != "Authorization" && key != "Origin" && key != "Referer" && key != "User-Agent" && key != "Accept" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("invalid plugin route header")
		}
		req.Header.Set(key, value)
	}
	client := &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &WebError{Code: "plugin_route_network_failed", Stage: "connection_or_headers"}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &WebError{Code: "plugin_route_http_rejected", Status: resp.StatusCode, Stage: "http_status"}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, &WebError{Code: "plugin_route_response_failed", Stage: "response_body"}
	}
	if int64(len(data)) > maxBytes {
		return nil, &WebError{Code: "response_too_large", Stage: "response_body"}
	}
	if !json.Valid(data) {
		return nil, &WebError{Code: "plugin_route_response_invalid", Stage: "response_body"}
	}
	return json.RawMessage(data), nil
}
