package pluginruntime

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// This subprocess is the current synthetic Go test binary. No Python, models,
// service installation, real account directory or external endpoint is used.
func TestManagedServiceFixture(t *testing.T) {
	mode := os.Getenv("WECHAT_MANAGED_SERVICE_FIXTURE")
	if mode == "" {
		return
	}
	if os.Getenv("WECHAT_SYNTHETIC_SECRET") != "" {
		os.Exit(9)
	}
	if mode == "exit" {
		os.Exit(0)
	}
	if mode == "stubborn" {
		time.Sleep(time.Minute)
		os.Exit(8)
	}
	s := bufio.NewScanner(os.Stdin)
	for s.Scan() {
		if s.Text() == "stop" {
			os.Exit(0)
		}
	}
	os.Exit(0)
}

func managedFixtureSpec(t *testing.T, mode string) ManagedServiceSpec {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return ManagedServiceSpec{
		OwnerPlugin: "synthetic", Binding: "synthetic-v1", Executable: executable,
		Arguments: []string{"-test.run=^TestManagedServiceFixture$"},
		Directory: dir, Environment: []string{"SystemRoot=" + os.Getenv("SystemRoot"), "WECHAT_MANAGED_SERVICE_FIXTURE=" + mode},
		LogPath: filepath.Join(dir, "service.log"), PendingJobsPath: filepath.Join(dir, "jobs.json"),
		StopInput: "stop\n", StopTimeout: 2 * time.Second,
		Check: func(ctx context.Context) error { return ctx.Err() },
	}
}

func fixtureAdmission(ctx context.Context, start func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return start()
}

func awaitManagedExit(t *testing.T, r *ManagedServices, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.Status(id).Running {
		if time.Now().After(deadline) {
			t.Fatal("synthetic supervisor did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestManagedServiceOwnedLifecycleBusyAndConfiguration(t *testing.T) {
	t.Setenv("WECHAT_SYNTHETIC_SECRET", "must-not-inherit")
	r := &ManagedServices{}
	spec := managedFixtureSpec(t, "wait")
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	if state, err := r.Start(t.Context(), "a", spec, fixtureAdmission); err != nil || !state.Running {
		t.Fatal(state, err)
	}
	for _, item := range []struct {
		data string
		busy bool
	}{
		{`{"a":{"state":"queued"}}`, true}, {`{"a":{"state":"running"}}`, true},
		{`{"a":{"state":"submitted"}}`, true}, {`{"a":{"state":"accepted"}}`, true},
		{`{"a":{"state":"unknown"}}`, true}, {`{"a":{"state":"completed"}}`, false},
		{`{"a":{"state":"cancelled"}}`, false}, {`invalid`, true}, {`null`, true},
		{`{"a":null}`, true}, {`{"a":{}}`, true},
	} {
		if err := os.WriteFile(spec.PendingJobsPath, []byte(item.data), 0600); err != nil {
			t.Fatal(err)
		}
		if r.Status("a").Busy != item.busy {
			t.Fatalf("wrong busy state for %s", item.data)
		}
	}
	// A repeated start returns ownership status without launching another child.
	if _, err := r.Start(t.Context(), "a", spec, func(context.Context, func() error) error { t.Fatal("duplicate child launch"); return nil }); err != nil {
		t.Fatal(err)
	}
	changed := spec
	changed.Binding = "changed"
	if _, err := r.Start(t.Context(), "a", changed, fixtureAdmission); err == nil {
		t.Fatal("changed registration adopted running child")
	}
	changed = spec
	changed.Check = func(context.Context) error { return errors.New("revoked") }
	if _, err := r.Start(t.Context(), "a", changed, fixtureAdmission); err == nil {
		t.Fatal("revoked registration returned success")
	}
	if err := r.Stop(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if state := r.Status("a"); state.Running || state.Busy {
		t.Fatal("stopped service retained busy state", state)
	}
	if _, err := r.Start(t.Context(), "a", spec, fixtureAdmission); err != nil {
		t.Fatal("restart after owned stop", err)
	}
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(t.Context(), "a", spec, fixtureAdmission); err == nil {
		t.Fatal("closed registry restarted")
	}
}

func TestManagedServiceIdleReclaimRenewsAdjacentRequestAndProtectsUncertainJob(t *testing.T) {
	r := &ManagedServices{}
	spec := managedFixtureSpec(t, "wait")
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	if _, err := r.Start(t.Context(), "music", spec, fixtureAdmission); err != nil {
		t.Fatal(err)
	}
	// A normal adjacent start must reuse the owned child and renew the lease.
	r.mu.Lock()
	r.processes["music"].lastActivity = time.Now().Add(-time.Hour)
	r.mu.Unlock()
	if _, err := r.Start(t.Context(), "music", spec, func(context.Context, func() error) error {
		t.Fatal("adjacent request launched a duplicate child")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if idle, ok := r.IdleFor("music", time.Now()); !ok || idle > time.Second {
		t.Fatalf("reuse did not renew idle lease: %v %v", idle, ok)
	}
	r.mu.Lock()
	r.processes["music"].lastActivity = time.Now().Add(-time.Hour)
	r.mu.Unlock()
	if err := os.WriteFile(spec.PendingJobsPath, []byte(`{"job":{"state":"accepted"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if stopped, err := r.StopIfIdle(t.Context(), "music", time.Minute); err != nil || stopped || !r.Status("music").Running {
		t.Fatalf("uncertain receipt was reclaimed: stopped=%v err=%v status=%+v", stopped, err, r.Status("music"))
	}
	if err := os.WriteFile(spec.PendingJobsPath, []byte(`{"job":{"state":"completed"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if stopped, err := r.StopIfIdle(t.Context(), "music", time.Minute); err != nil || !stopped {
		t.Fatalf("idle completed service not reclaimed: stopped=%v err=%v", stopped, err)
	}
	if r.Status("music").Running || r.Touch("music") {
		t.Fatal("stopped process remained touchable")
	}
}

func TestManagedServiceHoldPreventsIdleReclaimUntilTransportCompletes(t *testing.T) {
	r := &ManagedServices{}
	spec := managedFixtureSpec(t, "wait")
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	if _, err := r.Start(t.Context(), "music", spec, fixtureAdmission); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.processes["music"].lastActivity = time.Now().Add(-time.Hour)
	r.mu.Unlock()
	release, ok := r.Acquire("music")
	if !ok || !r.Status("music").Busy {
		t.Fatal("active transport did not acquire an owned service lease")
	}
	if stopped, err := r.StopIfIdle(t.Context(), "music", time.Minute); err != nil || stopped || !r.Status("music").Running {
		t.Fatalf("idle reaper reclaimed active transport: stopped=%v err=%v status=%+v", stopped, err, r.Status("music"))
	}
	release()
	r.mu.Lock()
	r.processes["music"].lastActivity = time.Now().Add(-time.Hour)
	r.mu.Unlock()
	if stopped, err := r.StopIfIdle(t.Context(), "music", time.Minute); err != nil || !stopped {
		t.Fatalf("idle service remained after transport release: stopped=%v err=%v", stopped, err)
	}
}

func TestManagedServiceGuardRejectsStaleIdleTimer(t *testing.T) {
	r := &ManagedServices{}
	spec := managedFixtureSpec(t, "wait")
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	if _, err := r.Start(t.Context(), "music", spec, fixtureAdmission); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.processes["music"].lastActivity = time.Now().Add(-time.Hour)
	r.mu.Unlock()
	if stopped, err := r.StopIfIdleGuarded(t.Context(), "music", time.Minute, func() bool { return false }); err != nil || stopped || !r.Status("music").Running {
		t.Fatalf("stale timer stopped owned service: stopped=%v err=%v", stopped, err)
	}
	if stopped, err := r.StopIfIdleGuarded(t.Context(), "music", time.Minute, func() bool { return true }); err != nil || !stopped {
		t.Fatalf("current timer did not reclaim idle service: stopped=%v err=%v", stopped, err)
	}
}

func TestManagedServiceCancellationDrainRaceAndCrashRestart(t *testing.T) {
	r := &ManagedServices{}
	spec := managedFixtureSpec(t, "exit")
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Start(cancelled, "a", spec, fixtureAdmission); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := r.Start(t.Context(), "a", spec, func(context.Context, func() error) error { return errors.New("draining") }); err == nil || r.Status("a").Running {
		t.Fatal("drain admitted child", err)
	}
	checked := 0
	changed := spec
	changed.Check = func(context.Context) error {
		checked++
		if checked == 2 {
			return errors.New("registration changed during preparation")
		}
		return nil
	}
	if _, err := r.Start(t.Context(), "a", changed, func(context.Context, func() error) error {
		t.Fatal("changed registration reached final admission")
		return nil
	}); err == nil || r.Status("a").Running {
		t.Fatal("changed registration admitted child", err)
	}
	if _, err := r.Start(t.Context(), "a", spec, fixtureAdmission); err != nil {
		t.Fatal(err)
	}
	awaitManagedExit(t, r, "a")
	if _, err := r.Start(t.Context(), "a", spec, fixtureAdmission); err != nil {
		t.Fatal("restart after crash", err)
	}
	awaitManagedExit(t, r, "a")
}

func TestManagedServiceShutdownIsBoundedAndConcurrentStartCannotAdoptStopping(t *testing.T) {
	r := &ManagedServices{}
	spec := managedFixtureSpec(t, "stubborn")
	spec.StopTimeout = 250 * time.Millisecond
	if _, err := r.Start(t.Context(), "a", spec, fixtureAdmission); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			if err := r.Stop(t.Context(), "a"); err != nil {
				t.Error(err)
			}
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for !r.Status("a").Stopping {
		if time.Now().After(deadline) {
			t.Fatal("stop not reserved")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := r.Start(t.Context(), "a", spec, fixtureAdmission); err == nil {
		t.Fatal("start adopted stopping service")
	}
	wg.Wait()
	if time.Since(start) > 3*time.Second {
		t.Fatal("owned shutdown exceeded bound")
	}
	if r.Status("a").Running {
		t.Fatal("owned supervisor remains running")
	}
}

func TestManagedServiceAdmissionErrorCannotOrphanStartedProcess(t *testing.T) {
	r := &ManagedServices{}
	spec := managedFixtureSpec(t, "wait")
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	if _, err := r.Start(t.Context(), "a", spec, func(context.Context, func() error) error { return nil }); err == nil {
		t.Fatal("admission without launch accepted")
	}
	if _, err := r.Start(t.Context(), "a", spec, func(_ context.Context, start func() error) error {
		if err := start(); err != nil {
			return err
		}
		return errors.New("failure after launch")
	}); err == nil {
		t.Fatal("admission error lost")
	}
	r.mu.Lock()
	tracked := r.processes["a"] != nil
	r.mu.Unlock()
	if !tracked {
		t.Fatal("started subprocess ownership lost")
	}
	awaitManagedExit(t, r, "a")
}

func TestManagedServiceStopOwnerIsolatesOwnersAndRefusesBusyBeforeAnyStop(t *testing.T) {
	r, foreign := &ManagedServices{}, &ManagedServices{}
	defer func() { _ = r.Close(context.Background()); _ = foreign.Close(context.Background()) }()
	start := func(registry *ManagedServices, id, owner string) ManagedServiceSpec {
		spec := managedFixtureSpec(t, "wait")
		spec.OwnerPlugin = owner
		if _, err := registry.Start(t.Context(), id, spec, fixtureAdmission); err != nil {
			t.Fatal(err)
		}
		return spec
	}
	start(r, "alpha-idle", "alpha")
	alphaBusy := start(r, "alpha-busy", "alpha")
	betaBusy := start(r, "beta-busy", "beta")
	start(foreign, "outside-registry", "alpha")
	for _, path := range []string{alphaBusy.PendingJobsPath, betaBusy.PendingJobsPath} {
		if err := os.WriteFile(path, []byte(`{"job":{"state":"queued"}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if !r.OwnerBusy("alpha") || !r.OwnerBusy("beta") || r.OwnerBusy("unknown") {
		t.Fatal("wrong per-owner busy state")
	}
	if err := r.StopOwner(t.Context(), "alpha"); err == nil {
		t.Fatal("pending owner stopped")
	}
	if !r.Status("alpha-idle").Running || !r.Status("alpha-busy").Running {
		t.Fatal("busy refusal partially stopped owner")
	}
	if err := os.WriteFile(alphaBusy.PendingJobsPath, []byte(`{"job":{"state":"completed"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.StopOwner(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if r.Status("alpha-idle").Running || r.Status("alpha-busy").Running || r.OwnerBusy("alpha") {
		t.Fatal("owner resources retained")
	}
	if !r.Status("beta-busy").Running || !r.OwnerBusy("beta") || !foreign.Status("outside-registry").Running {
		t.Fatal("another owner's or external process stopped")
	}
	if err := r.StopOwner(t.Context(), "unknown"); err != nil {
		t.Fatal(err)
	}
	if !r.Status("beta-busy").Running {
		t.Fatal("unknown owner changed live process")
	}
}

func TestManagedServiceConcurrentStopOwnerReservesOnlyThatOwner(t *testing.T) {
	r := &ManagedServices{}
	defer func() { _ = r.Close(context.Background()) }()
	spec := managedFixtureSpec(t, "stubborn")
	spec.OwnerPlugin = "alpha"
	spec.StopTimeout = 250 * time.Millisecond
	if _, err := r.Start(t.Context(), "alpha", spec, fixtureAdmission); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.StopOwner(t.Context(), "alpha"); err != nil {
				t.Error(err)
			}
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for !r.Status("alpha").Stopping {
		if time.Now().After(deadline) {
			t.Fatal("owner stop not reserved")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := r.Start(t.Context(), "another-alpha", spec, fixtureAdmission); err == nil {
		t.Fatal("owner start raced stop")
	}
	beta := managedFixtureSpec(t, "wait")
	beta.OwnerPlugin = "beta"
	if _, err := r.Start(t.Context(), "beta", beta, fixtureAdmission); err != nil {
		t.Fatal("other owner blocked by stop", err)
	}
	wg.Wait()
	if r.Status("alpha").Running || !r.Status("beta").Running {
		t.Fatal("wrong owner stopped")
	}
	// A completed owner stop does not permanently disable future explicitly
	// authorized starts; Controller controls the persisted plugin enabled state.
	if _, err := r.Start(t.Context(), "alpha", spec, fixtureAdmission); err != nil {
		t.Fatal("owner reservation retained after stop", err)
	}
}
