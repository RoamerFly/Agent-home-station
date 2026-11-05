package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type executorFunc func(context.Context, Package, Grant) (Session, error)

func (f executorFunc) Start(c context.Context, p Package, g Grant) (Session, error) {
	return f(c, p, g)
}

type controlledSession struct {
	entered, release chan struct{}
	closes           atomic.Int32
	failFirst        bool
}

func (s *controlledSession) Health(context.Context) error { return nil }
func (s *controlledSession) Call(context.Context, Invocation, string, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (s *controlledSession) Close() error {
	n := s.closes.Add(1)
	if n == 1 && s.entered != nil {
		close(s.entered)
		<-s.release
	}
	if n == 1 && s.failFirst {
		return errors.New("termination unknown")
	}
	return nil
}
func registerControlled(t *testing.T, h *Host, id string) {
	t.Helper()
	if err := h.Register(Registration{Package: Package{ID: id, Version: "1", Status: "ready"}, Grant: Grant{Account: "a", Plugin: id, Version: "1"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
}
func awaitResult(t *testing.T, c <-chan error) error {
	t.Helper()
	select {
	case err := <-c:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("host operation blocked")
		return nil
	}
}

func TestDisableReservesStateWithoutBlockingOtherPlugins(t *testing.T) {
	slow := &controlledSession{entered: make(chan struct{}), release: make(chan struct{})}
	h, _ := New("a", executorFunc(func(_ context.Context, p Package, _ Grant) (Session, error) {
		if p.ID == "slow" {
			return slow, nil
		}
		return &controlledSession{}, nil
	}))
	registerControlled(t, h, "slow")
	registerControlled(t, h, "fast")
	for _, id := range []string{"slow", "fast"} {
		if err := h.Start(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	stopped := make(chan error, 1)
	go func() { stopped <- h.Disable("slow") }()
	<-slow.entered
	checked := make(chan error, 1)
	go func() {
		if h.Start(context.Background(), "slow") == nil {
			checked <- errors.New("restarted during disable")
			return
		}
		_, err := h.Call(context.Background(), "fast", Invocation{Account: "a", Thread: "t", Requester: "u", Purpose: "test", RequestKey: "k"}, "echo", json.RawMessage(`{}`), nil)
		checked <- err
	}()
	err := awaitResult(t, checked)
	close(slow.release)
	if err != nil {
		t.Fatal(err)
	}
	if err = awaitResult(t, stopped); err != nil {
		t.Fatal(err)
	}
	if h.Start(context.Background(), "slow") == nil {
		t.Fatal("disabled plugin restarted")
	}
	if err = h.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFailedCloseRemainsUnavailableButCanRetry(t *testing.T) {
	s := &controlledSession{failFirst: true}
	h, _ := New("a", executorFunc(func(context.Context, Package, Grant) (Session, error) { return s, nil }))
	registerControlled(t, h, "sample")
	if err := h.Start(context.Background(), "sample"); err != nil {
		t.Fatal(err)
	}
	if h.Stop("sample") == nil {
		t.Fatal("failed close swallowed")
	}
	if h.Start(context.Background(), "sample") == nil {
		t.Fatal("unconfirmed process replaced")
	}
	if _, err := h.Call(context.Background(), "sample", Invocation{Account: "a", Thread: "t", Requester: "u", Purpose: "test", RequestKey: "k"}, "echo", nil, nil); err == nil {
		t.Fatal("unconfirmed process reused")
	}
	if err := h.Stop("sample"); err != nil {
		t.Fatal(err)
	}
	if s.closes.Load() != 2 {
		t.Fatal("close not retried")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseDuringStartupPreventsOrphanAndReplacement(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s := &controlledSession{failFirst: true}
	h, _ := New("a", executorFunc(func(context.Context, Package, Grant) (Session, error) { close(entered); <-release; return s, nil }))
	registerControlled(t, h, "sample")
	started := make(chan error, 1)
	go func() { started <- h.Start(context.Background(), "sample") }()
	<-entered
	if h.Close() == nil {
		t.Fatal("incomplete startup drain reported successful")
	}
	close(release)
	if awaitResult(t, started) == nil {
		t.Fatal("startup succeeded after shutdown")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if s.closes.Load() != 2 {
		t.Fatal("unconfirmed startup cleanup not retried")
	}
}
