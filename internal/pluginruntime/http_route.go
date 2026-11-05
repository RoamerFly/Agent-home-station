package pluginruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"wechat-bot/internal/pluginhost"
)

type RouteTransport func(context.Context, string, map[string]string, json.RawMessage, int64, time.Duration) (json.RawMessage, error)

// ProxyHTTPRoute is used inside ServicePolicy.Service, after serviceContext's
// authorization check. Binding must check source-specific payload fields. There
// is deliberately no default "network permission means any object" binding.
func ProxyHTTPRoute(ctx context.Context, store *pluginhost.Store, job pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage, bind func(string, json.RawMessage) error, send RouteTransport) (json.RawMessage, error) {
	if bind == nil || send == nil {
		return nil, errors.New("HTTP route binding unavailable")
	}
	var in struct {
		Route    string          `json:"route"`
		Body     json.RawMessage `json:"body"`
		MaxBytes int64           `json:"max_bytes"`
		Timeout  int64           `json:"timeout_seconds"`
		Referer  string          `json:"referer,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || in.MaxBytes < 1 || in.MaxBytes > 4<<20 || in.Timeout < 1 || in.Timeout > int64((1<<63-1)/time.Second) {
		return nil, errors.New("invalid HTTP route arguments")
	}
	if err := bind(in.Route, in.Body); err != nil {
		return nil, errors.New("HTTP route source binding rejected")
	}
	req, err := store.ResolveHTTPRequest(ctx, job, cap, in.Route, in.Body, in.Referer)
	if err != nil {
		return nil, err
	}
	return send(ctx, req.Endpoint, req.Headers, req.Body, in.MaxBytes, time.Duration(in.Timeout)*time.Second)
}
