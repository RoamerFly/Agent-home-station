package pluginruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"wechat-bot/internal/pluginhost"
)

func TestHTTPRouteRequiresSourceBindingAndRejectsDestinationOverrides(t *testing.T) {
	calls := 0
	send := func(context.Context, string, map[string]string, json.RawMessage, int64, time.Duration) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	}
	valid := json.RawMessage(`{"route":"read","body":{"id":"synthetic"},"max_bytes":1000,"timeout_seconds":25}`)
	if _, err := ProxyHTTPRoute(context.Background(), nil, pluginhost.Job{}, pluginhost.Capability{}, valid, nil, send); err == nil {
		t.Fatal("no source binding accepted")
	}
	if _, err := ProxyHTTPRoute(context.Background(), nil, pluginhost.Job{}, pluginhost.Capability{}, valid, func(string, json.RawMessage) error { return errors.New("wrong original") }, send); err == nil {
		t.Fatal("rejected source submitted")
	}
	for _, raw := range []string{
		`{"route":"read","body":{},"max_bytes":1000,"timeout_seconds":25,"url":"https://evil.example"}`,
		`{"route":"read","body":{},"max_bytes":1000,"timeout_seconds":25,"headers":{"Cookie":"stolen"}}`,
		`{"route":"read","body":{},"max_bytes":1000,"timeout_seconds":25} {}`,
		`{"route":"read","body":{},"max_bytes":0,"timeout_seconds":25}`,
	} {
		if _, err := ProxyHTTPRoute(context.Background(), nil, pluginhost.Job{}, pluginhost.Capability{}, json.RawMessage(raw), func(string, json.RawMessage) error { t.Fatal("invalid envelope reached source policy"); return nil }, send); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid requests reached network")
	}
}
