package pluginruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"wechat-bot/internal/pluginhost"
)

// queuedSession is a pure in-memory plugin Session.  Tests deliberately block
// Call without a subprocess so they can prove Controller's reservation rules
// without invoking a provider or device backend.
type queuedSession struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
	block   bool
}

func (s *queuedSession) Health(context.Context) error { return nil }
func (s *queuedSession) Close() error                 { return nil }
func (s *queuedSession) Call(ctx context.Context, _ pluginhost.Invocation, _ string, _ json.RawMessage) (json.RawMessage, error) {
	s.mu.Lock()
	s.calls++
	block, entered, release := s.block, s.entered, s.release
	s.mu.Unlock()
	if block {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return json.RawMessage(`{"ok":true}`), nil
}
func (s *queuedSession) Calls() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

func queuedController(t *testing.T, ids ...string) (*Controller, map[string]pluginhost.InstalledPackage, map[string]*queuedSession) {
	t.Helper()
	graph := map[string][]string{}
	for _, id := range ids {
		graph[id] = nil
	}
	store, installed := dependencyFixture(t, graph)
	sessions := map[string]*queuedSession{}
	for _, id := range ids {
		sessions[id] = &queuedSession{}
	}
	c, err := NewController(store, startFunc(func(_ context.Context, p pluginhost.Package, _ pluginhost.Grant) (pluginhost.Session, error) {
		return sessions[p.ID], nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	for _, id := range ids {
		if err = c.Start(context.Background(), id, installed[id].Revision); err != nil {
			t.Fatal(err)
		}
	}
	return c, installed, sessions
}

func queuedRequest(plugin, key string) CallRequest {
	inv := invocation()
	inv.RequestKey = key
	return CallRequest{Plugin: plugin, Capability: "echo", Context: inv, Arguments: json.RawMessage(`{"text":"queued"}`)}
}

func receiveResult(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for invocation")
		return nil
	}
}

func TestInvokeQueuesSamePluginWithoutReplay(t *testing.T) {
	c, _, sessions := queuedController(t, "one")
	s := sessions["one"]
	s.block, s.entered, s.release = true, make(chan struct{}, 1), make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := c.Invoke(context.Background(), queuedRequest("one", "first"), invocationPolicy{})
		firstDone <- err
	}()
	<-s.entered
	secondDone := make(chan error, 1)
	go func() {
		_, err := c.Invoke(context.Background(), queuedRequest("one", "second"), invocationPolicy{})
		secondDone <- err
	}()
	// A second process call while the first session is blocked would prove the
	// reservation was released too early.  The only sender is Call, so this is
	// a bounded event wait rather than a polling sleep.
	select {
	case <-s.entered:
		t.Fatal("same-plugin invocation reached the session before release")
	case <-time.After(80 * time.Millisecond):
	}
	close(s.release)
	if err := receiveResult(t, firstDone); err != nil {
		t.Fatal(err)
	}
	if err := receiveResult(t, secondDone); err != nil {
		t.Fatal(err)
	}
	if s.Calls() != 2 {
		t.Fatalf("queued calls = %d, want 2", s.Calls())
	}
}

func TestInvokeDifferentPluginsDoNotBlock(t *testing.T) {
	c, _, sessions := queuedController(t, "one", "two")
	sessions["one"].block, sessions["one"].entered, sessions["one"].release = true, make(chan struct{}, 1), make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := c.Invoke(context.Background(), queuedRequest("one", "first"), invocationPolicy{})
		firstDone <- err
	}()
	<-sessions["one"].entered
	twoDone := make(chan error, 1)
	go func() {
		_, err := c.Invoke(context.Background(), queuedRequest("two", "other-plugin"), invocationPolicy{})
		twoDone <- err
	}()
	if err := receiveResult(t, twoDone); err != nil {
		t.Fatal(err)
	}
	if sessions["two"].Calls() != 1 {
		t.Fatal("independent plugin was held behind busy plugin")
	}
	close(sessions["one"].release)
	if err := receiveResult(t, firstDone); err != nil {
		t.Fatal(err)
	}
}

func TestInvokeWaitingCancellationDoesNotSubmit(t *testing.T) {
	c, _, sessions := queuedController(t, "one")
	s := sessions["one"]
	s.block, s.entered, s.release = true, make(chan struct{}, 1), make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := c.Invoke(context.Background(), queuedRequest("one", "first"), invocationPolicy{})
		firstDone <- err
	}()
	<-s.entered
	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := c.Invoke(ctx, queuedRequest("one", "cancelled"), invocationPolicy{})
		secondDone <- err
	}()
	cancel()
	if err := receiveResult(t, secondDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting call error = %v", err)
	}
	if s.Calls() != 1 {
		t.Fatal("cancelled waiter submitted a call")
	}
	close(s.release)
	if err := receiveResult(t, firstDone); err != nil {
		t.Fatal(err)
	}
}

func TestInvokeWakeOnCloseAndRechecksPolicy(t *testing.T) {
	t.Run("close", func(t *testing.T) {
		c, _, sessions := queuedController(t, "one")
		s := sessions["one"]
		s.block, s.entered, s.release = true, make(chan struct{}, 1), make(chan struct{})
		firstDone := make(chan error, 1)
		go func() {
			_, err := c.Invoke(context.Background(), queuedRequest("one", "first"), invocationPolicy{})
			firstDone <- err
		}()
		<-s.entered
		waiting := make(chan error, 1)
		go func() {
			_, err := c.Invoke(context.Background(), queuedRequest("one", "waiting"), invocationPolicy{})
			waiting <- err
		}()
		// Host.Close may report its already-active call, but Controller marks
		// itself closed and broadcasts before delegating to Host.Close.
		_ = c.Close()
		if err := receiveResult(t, waiting); err == nil || err.Error() != "plugin controller closed" {
			t.Fatalf("waiter after Close = %v", err)
		}
		close(s.release)
		if err := receiveResult(t, firstDone); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("revoked", func(t *testing.T) {
		c, _, sessions := queuedController(t, "one")
		s := sessions["one"]
		s.block, s.entered, s.release = true, make(chan struct{}, 1), make(chan struct{})
		firstDone := make(chan error, 1)
		go func() {
			_, err := c.Invoke(context.Background(), queuedRequest("one", "first"), invocationPolicy{})
			firstDone <- err
		}()
		<-s.entered
		waiting := make(chan error, 1)
		go func() {
			_, err := c.Invoke(context.Background(), queuedRequest("one", "revoked"), invocationPolicy{check: func(pluginhost.Job) error { return errors.New("source revoked") }})
			waiting <- err
		}()
		close(s.release)
		if err := receiveResult(t, firstDone); err != nil {
			t.Fatal(err)
		}
		if err := receiveResult(t, waiting); err == nil || err.Error() != "source revoked" {
			t.Fatalf("waiter was not rechecked: %v", err)
		}
		if s.Calls() != 1 {
			t.Fatalf("revoked waiter reached plugin: %d", s.Calls())
		}
	})
}

func TestInvokeQueuedSameKeyReusesCompletedJob(t *testing.T) {
	c, _, sessions := queuedController(t, "one")
	s := sessions["one"]
	s.block, s.entered, s.release = true, make(chan struct{}, 1), make(chan struct{})
	request := queuedRequest("one", "same-request")
	firstDone := make(chan CallResult, 1)
	firstErr := make(chan error, 1)
	go func() {
		got, err := c.Invoke(context.Background(), request, invocationPolicy{})
		firstDone <- got
		firstErr <- err
	}()
	<-s.entered
	secondDone := make(chan CallResult, 1)
	secondErr := make(chan error, 1)
	go func() {
		got, err := c.Invoke(context.Background(), request, invocationPolicy{})
		secondDone <- got
		secondErr <- err
	}()
	close(s.release)
	if err := receiveResult(t, firstErr); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstDone:
	case <-time.After(3 * time.Second):
		t.Fatal("first result missing")
	}
	if err := receiveResult(t, secondErr); err != nil {
		t.Fatal(err)
	}
	var second CallResult
	select {
	case second = <-secondDone:
	case <-time.After(3 * time.Second):
		t.Fatal("second result missing")
	}
	if !second.Reused || s.Calls() != 1 {
		t.Fatalf("same request replayed: reused=%v calls=%d", second.Reused, s.Calls())
	}
}
