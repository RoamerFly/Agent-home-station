package pluginruntime

import (
	"context"
	"encoding/json"
	"testing"
	"wechat-bot/internal/pluginhost"
)

func TestStopSessionRetainsEnabledAndRestart(t *testing.T) {
	s, i := installedFixture(t)
	calls := 0
	c, err := NewController(s, startFunc(func(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
		calls++
		return &invocationSession{call: func(pluginhost.Invocation, string, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.Start(t.Context(), i.Package.ID, i.Revision); err != nil {
		t.Fatal(err)
	}
	current, _ := s.Installed(t.Context(), i.Package.ID)
	if err = c.StopSession(t.Context(), i.Package.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Installed(t.Context(), i.Package.ID)
	if !after.Enabled || after.Revision != current.Revision || c.Status(i.Package.ID).State != "stopped" {
		t.Fatal("stop changed enable intent", after, c.Status(i.Package.ID))
	}
	if err = c.Start(t.Context(), i.Package.ID, after.Revision); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
	if err = c.Stop(t.Context(), i.Package.ID, after.Revision); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Installed(t.Context(), i.Package.ID)
	if after.Enabled {
		t.Fatal("disable retained permission")
	}
}
