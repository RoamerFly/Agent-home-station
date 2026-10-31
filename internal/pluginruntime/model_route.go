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

// ModelMaterializer is host-owned. It validates the requested provider model,
// prompts/parameters and source references, then replaces only authorized media
// references with actual bytes. Plugins never supply host paths or credentials.
type ModelMaterializer func(context.Context, string, json.RawMessage) (json.RawMessage, error)

// ProxyModelRoute uses a distinct permission and mandatory source materializer.
// Media stays in the host; only a small reference request crosses MCP. No model
// call is retried here, including when transport returns an unknown result.
func ProxyModelRoute(ctx context.Context, store *pluginhost.Store, job pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage, materialize ModelMaterializer, send RouteTransport) (json.RawMessage, error) {
	if materialize == nil || send == nil {
		return nil, errors.New("model source binding unavailable")
	}
	var in struct {
		Route    string          `json:"route"`
		Body     json.RawMessage `json:"body"`
		MaxBytes int64           `json:"max_bytes"`
		Timeout  int64           `json:"timeout_seconds"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 1<<20 || uniqueJSON(raw) != nil || d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || in.MaxBytes < 1 || in.MaxBytes > 4<<20 || in.Timeout < 1 || in.Timeout > int64((1<<63-1)/time.Second) {
		return nil, errors.New("invalid model route arguments")
	}
	// Check permission/configuration before touching private media, then resolve
	// again after materialization so a concurrent configuration change fails.
	if _, err := store.ResolveModelRequest(ctx, job, cap, in.Route, json.RawMessage(`{}`)); err != nil {
		return nil, err
	}
	body, err := materialize(ctx, in.Route, in.Body)
	if err != nil {
		return nil, errors.New("model source binding rejected")
	}
	if len(body) > 32<<20 || uniqueJSON(body) != nil {
		return nil, errors.New("invalid materialized model request")
	}
	req, err := store.ResolveModelRequest(ctx, job, cap, in.Route, body)
	if err != nil {
		return nil, err
	}
	return send(ctx, req.Endpoint, req.Headers, req.Body, in.MaxBytes, time.Duration(in.Timeout)*time.Second)
}
