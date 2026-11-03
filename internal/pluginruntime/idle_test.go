package pluginruntime

import (
	"context"
	"sync"
	"testing"
	"time"

	"wechat-bot/internal/pluginhost"
)

type idleTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *idleTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *idleTestClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }
func (c *idleTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func onDemandController(t *testing.T) (*Controller, *pluginhost.Store, pluginhost.InstalledPackage, *idleTestClock, *int, *int) {
	t.Helper()
	store, installed := installedFixture(t)
	clock := &idleTestClock{now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	starts, closes := 0, 0
	c, err := newControllerWithClock(store, startFunc(func(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
		starts++
		return &ensureSession{alive: true, closed: &closes}, nil
	}), clock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, store, installed, clock, &starts, &closes
}

func TestOnDemandIdleRecycleKeepsEnabledAndRestarts(t *testing.T) {
	c, store, installed, clock, starts, closes := onDemandController(t)
	if err := c.EnableOnDemand(t.Context(), installed.Package.ID, installed.Revision); err != nil {
		t.Fatal(err)
	}
	current, err := store.Installed(t.Context(), installed.Package.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.EnsureStarted(t.Context(), current.Package.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
	if status := c.Status(current.Package.ID); !status.AutoManaged || status.IdleDeadline == "" || status.State != "running" {
		t.Fatalf("on-demand status missing idle lease: %+v", status)
	}
	clock.Advance(10 * time.Minute)
	c.reapIdle(t.Context())
	after, err := store.Installed(t.Context(), current.Package.ID)
	if err != nil || !after.Enabled || c.Status(current.Package.ID).State != "stopped" || c.Status(current.Package.ID).AutoManaged || *closes != 1 {
		t.Fatalf("idle recycle changed enable intent or status: installed=%+v status=%+v closes=%d err=%v", after, c.Status(current.Package.ID), *closes, err)
	}
	if err = c.EnsureStarted(t.Context(), after.Package.ID, after.Revision); err != nil || *starts != 2 || c.Status(after.Package.ID).State != "running" {
		t.Fatalf("next independent request did not cold-start: starts=%d status=%+v err=%v", *starts, c.Status(after.Package.ID), err)
	}
}

func TestExplicitStartPinsAutomaticProcessUntilReturnedToOnDemand(t *testing.T) {
	c, store, installed, clock, _, closes := onDemandController(t)
	if err := c.EnableOnDemand(t.Context(), installed.Package.ID, installed.Revision); err != nil {
		t.Fatal(err)
	}
	current, _ := store.Installed(t.Context(), installed.Package.ID)
	if err := c.EnsureStarted(t.Context(), current.Package.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(t.Context(), current.Package.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
	clock.Advance(11 * time.Minute)
	c.reapIdle(t.Context())
	if c.Status(current.Package.ID).State != "running" || c.Status(current.Package.ID).AutoManaged || *closes != 0 {
		t.Fatalf("explicit start was not resident: %+v closes=%d", c.Status(current.Package.ID), *closes)
	}
	if err := c.EnableOnDemand(t.Context(), current.Package.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
	clock.Advance(10 * time.Minute)
	c.reapIdle(t.Context())
	if c.Status(current.Package.ID).State != "stopped" || *closes != 1 {
		t.Fatalf("return to on-demand did not recycle: %+v closes=%d", c.Status(current.Package.ID), *closes)
	}
}

func TestIdleReaperRetainsUnfinishedJobWithoutRefreshingLease(t *testing.T) {
	c, store, installed, clock, _, closes := onDemandController(t)
	if err := c.EnableOnDemand(t.Context(), installed.Package.ID, installed.Revision); err != nil {
		t.Fatal(err)
	}
	current, _ := store.Installed(t.Context(), installed.Package.ID)
	if err := c.EnsureStarted(t.Context(), current.Package.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
	_, created, err := store.CreateJob(t.Context(), pluginhost.Job{
		Plugin: current.Package.ID, Version: current.Package.Version, PackageDigest: current.Digest,
		ConfigRevision: "0", Tool: "echo", Context: invocation(), Arguments: []byte(`{"text":"pending"}`),
	})
	if err != nil || !created {
		t.Fatalf("cannot create synthetic pending job: created=%v err=%v", created, err)
	}
	clock.Advance(10 * time.Minute)
	c.reapIdle(t.Context())
	if c.Status(current.Package.ID).State != "running" || *closes != 0 {
		t.Fatalf("unfinished job process was reclaimed: %+v closes=%d", c.Status(current.Package.ID), *closes)
	}
	if pending, err := store.HasUnfinishedJobs(t.Context(), current.Package.ID); err != nil || !pending {
		t.Fatalf("unfinished-job query lost its conservative state: pending=%v err=%v", pending, err)
	}
}

func TestIdleReaperDoesNotCloseBusyOrSharedPinnedDependency(t *testing.T) {
	store, items := dependencyFixture(t, map[string][]string{"left": {"leaf"}, "right": {"leaf"}, "leaf": nil})
	clock := &idleTestClock{now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	closes := map[string]*int{}
	c, err := newControllerWithClock(store, startFunc(func(_ context.Context, p pluginhost.Package, _ pluginhost.Grant) (pluginhost.Session, error) {
		if closes[p.ID] == nil {
			closes[p.ID] = new(int)
		}
		return &ensureSession{alive: true, closed: closes[p.ID]}, nil
	}), clock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// A manual right-hand root owns leaf.  An on-demand left request reuses leaf
	// but must not turn that shared process into an idle-reclaimable one.
	if err = c.Start(t.Context(), "right", items["right"].Revision); err != nil {
		t.Fatal(err)
	}
	if err = c.EnableOnDemand(t.Context(), "left", items["left"].Revision); err != nil {
		t.Fatal(err)
	}
	if err = c.EnsureStarted(t.Context(), "left", items["left"].Revision); err != nil {
		t.Fatal(err)
	}
	clock.Advance(10 * time.Minute)
	if err = c.begin("left"); err != nil { // an old timer sees an in-flight request
		t.Fatal(err)
	}
	c.reapIdle(t.Context())
	c.end("left", nil)
	if c.Status("left").State != "running" || c.Status("leaf").State != "running" || *closes["left"] != 0 || *closes["leaf"] != 0 {
		t.Fatalf("old timer stopped active/shared session: left=%+v leaf=%+v closes=%#v", c.Status("left"), c.Status("leaf"), closes)
	}
	// Once the request is no longer active, only left is reclaimed; right's
	// explicit pin continues to protect the shared dependency.
	c.reapIdle(t.Context())
	if c.Status("left").State != "stopped" || c.Status("leaf").State != "running" || *closes["leaf"] != 0 {
		t.Fatalf("shared dependency was reclaimed while manually pinned: left=%+v leaf=%+v closes=%#v", c.Status("left"), c.Status("leaf"), closes)
	}
}
