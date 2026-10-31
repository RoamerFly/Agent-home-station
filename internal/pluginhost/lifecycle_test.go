package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type fakeExecutor struct {
	session *fakeSession
	starts  int
}

func (e *fakeExecutor) Start(context.Context, Package, Grant) (Session, error) {
	e.starts++
	return e.session, nil
}

type fakeSession struct {
	calls, closed int
	failure       bool
}

func (s *fakeSession) Health(context.Context) error { return nil }
func (s *fakeSession) Call(context.Context, Invocation, string, json.RawMessage) (json.RawMessage, error) {
	s.calls++
	if s.failure {
		return nil, errors.New("interrupted outcome unknown")
	}
	return json.RawMessage(`{"ok":true}`), nil
}
func (s *fakeSession) Close() error { s.closed++; return nil }
func TestHostIsolationDisabledAndNoReplay(t *testing.T) {
	ctx := context.Background()
	executor := &fakeExecutor{session: &fakeSession{failure: true}}
	h, _ := New("a", executor)
	defer h.Close()
	r := Registration{Package: Package{ID: "sample", Version: "1", Status: "ready", Permissions: []string{"artifact.read"}}, Grant: Grant{Account: "a", Plugin: "sample", Version: "1", Permissions: []string{"artifact.read"}}, Enabled: true}
	if err := h.Register(r); err != nil {
		t.Fatal(err)
	}
	r.Grant.Permissions[0] = "network" // Must not modify registered grant.
	if err := h.Start(ctx, "sample"); err != nil {
		t.Fatal(err)
	}
	c := Invocation{Account: "b", Thread: "t", Requester: "u", Purpose: "test", RequestKey: "k"}
	if _, err := h.Call(ctx, "sample", c, "read", nil, []string{"artifact.read"}); err == nil {
		t.Fatal("cross-account call")
	}
	c.Account = "a"
	if _, err := h.Call(ctx, "sample", c, "read", nil, []string{"artifact.read"}); err == nil {
		t.Fatal("failure swallowed")
	}
	if executor.session.calls != 1 {
		t.Fatal("call replayed")
	}
	if err := h.Disable("sample"); err != nil {
		t.Fatal(err)
	}
	if h.Start(ctx, "sample") == nil {
		t.Fatal("disabled plugin started")
	}
}
func TestHostFailsClosedWithoutExecutor(t *testing.T) {
	h, _ := New("a", nil)
	r := Registration{Package: Package{ID: "sample", Version: "1", Status: "ready"}, Grant: Grant{Account: "a", Plugin: "sample", Version: "1"}, Enabled: true}
	if err := h.Register(r); err != nil {
		t.Fatal(err)
	}
	if h.Start(context.Background(), "sample") == nil {
		t.Fatal("missing isolation accepted")
	}
}
