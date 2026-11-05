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

func TestHostRejectsCallsAfterDependencyVersionChanges(t *testing.T) {
	ctx := context.Background()
	leaf, root := &fakeSession{}, &fakeSession{}
	h, _ := New("a", &fakeExecutor{session: root})
	defer h.Close()
	register := func(id, version string, dependencies Dependencies, session *fakeSession) {
		// Each Start needs a distinct session; the executor's pointer is only
		// used while starting this registration in the synthetic contract.
		h.executor = &fakeExecutor{session: session}
		p := Package{ID: id, Version: version, Status: "ready", Dependencies: dependencies}
		if err := h.Register(Registration{Package: p, Grant: Grant{Account: "a", Plugin: id, Version: version}, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if err := h.Start(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	register("leaf", "1.2.0", nil, leaf)
	register("root", "1.0.0", Dependencies{{ID: "leaf", Version: "^1.2.0"}}, root)
	inv := Invocation{Account: "a", Thread: "t", Requester: "u", Purpose: "synthetic", RequestKey: "k"}
	if _, err := h.Call(ctx, "root", inv, "echo", nil, nil); err != nil {
		t.Fatal(err)
	}
	// Controller registration is refreshed after an installed dependency is
	// upgraded. The live root must fail closed before dispatching another call.
	h.mu.Lock()
	h.items["leaf"].registration.Package.Version = "2.0.0"
	h.mu.Unlock()
	if _, err := h.Call(ctx, "root", inv, "echo", nil, nil); err == nil || root.calls != 1 {
		t.Fatal("incompatible upgraded dependency accepted", err, root.calls)
	}
}
