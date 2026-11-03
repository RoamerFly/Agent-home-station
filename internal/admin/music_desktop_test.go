package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"wechat-bot/internal/pluginruntime"
)

func TestWindowsMusicIdleFixture(t *testing.T) {
	if os.Getenv("WECHAT_WINDOWS_MUSIC_IDLE_FIXTURE") == "" {
		return
	}
	s := bufio.NewScanner(os.Stdin)
	for s.Scan() {
		if s.Text() == "stop" {
			return
		}
	}
}

func TestWindowsMusicPolicyScopeAndForeignPortRefused(t *testing.T) {
	f := setup(t)
	if _, err := f.manager.startWindowsMusic(t.Context()); err == nil || !strings.Contains(err.Error(), "not_installed_or_authorized") {
		t.Fatal("unregistered process admitted", err)
	}
	public := filepath.Join(f.manager.managedRoot(), "public_workspace")
	python := filepath.Join(public, "runtimes", "python", "python.exe")
	if err := os.MkdirAll(filepath.Dir(python), 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(python, []byte("synthetic executable"), 0600)
	control := t.TempDir()
	entry := filepath.Join(control, "managed_music_service.py")
	os.WriteFile(entry, []byte("synthetic control"), 0600)
	config := windowsMusicConfig{Enabled: true, Policy: windowsMusicPolicy, Python: python, Entry: entry, Media: control, IPC: control}
	save := func() {
		data, _ := json.Marshal(config)
		if err := os.WriteFile(filepath.Join(f.manager.dir, "windows-music.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	config.Policy = "unrestricted"
	save()
	if _, err := f.manager.startWindowsMusic(t.Context()); err == nil {
		t.Fatal("unknown policy admitted")
	}
	config.Policy = windowsMusicPolicy
	config.Entry = filepath.Join(public, "managed_music_service.py")
	os.WriteFile(config.Entry, []byte("agent visible control"), 0600)
	save()
	if _, err := f.manager.startWindowsMusic(t.Context()); err == nil || !strings.Contains(err.Error(), "control_inside") {
		t.Fatal("control plane mounted", err)
	}
	config.Entry = entry
	save()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.YuEEndpoint = "http://" + listener.Addr().String()
		legacySpeechFixture.YuEToken = "synthetic-local-token"
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	installLegacyLocalMusicPlugin(t, f.manager)
	if _, err := f.manager.startWindowsMusic(t.Context()); err == nil || !strings.Contains(err.Error(), "port_occupied") {
		t.Fatal("foreign listener adopted", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.manager.startWindowsMusic(ctx); err != context.Canceled {
		t.Fatal("cancelled launch admitted", err)
	}
	listener.Close()
	revoked := errors.New("synthetic source revoked during launch preparation")
	ctx = context.WithValue(t.Context(), musicServiceControlCheckKey{}, func(context.Context) error {
		// Simulate authority changing after the private credential has been
		// prepared, before the final process admission. The fake executable
		// must never be invoked, and no process may be adopted.
		if _, err := os.Stat(filepath.Join(f.manager.dir, "windows-music-token")); err == nil {
			return revoked
		}
		return nil
	})
	if _, err := f.manager.startWindowsMusic(ctx); !errors.Is(err, revoked) || f.manager.windowsMusicStatus().Running {
		t.Fatal("launch preparation bypassed current source check", err)
	}
}

func TestManagedServiceRegistryInitializationDoesNotAcquireManagerLock(t *testing.T) {
	m := &Manager{}
	m.mu.Lock()
	initialized := make(chan *pluginruntime.ManagedServices, 1)
	go func() { initialized <- m.managedServiceRegistry() }()
	var registry *pluginruntime.ManagedServices
	select {
	case registry = <-initialized:
		m.mu.Unlock()
	case <-time.After(2 * time.Second):
		m.mu.Unlock()
		t.Fatal("generic registry initialization waits on Manager.mu")
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if m.managedServiceRegistry() != registry {
				t.Error("multiple registry instances")
			}
		}()
	}
	wg.Wait()
}

func TestWindowsMusicIdleRecoveryOnlyReclaimsOwnedCompletedBackend(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	registry := &pluginruntime.ManagedServices{}
	m := &Manager{managedServices: registry}
	start := func(id string) string {
		jobs := filepath.Join(dir, id+".json")
		spec := pluginruntime.ManagedServiceSpec{
			OwnerPlugin: "music", Binding: id, Executable: executable,
			Arguments: []string{"-test.run=^TestWindowsMusicIdleFixture$"}, Directory: dir,
			Environment: []string{"SystemRoot=" + os.Getenv("SystemRoot"), "WECHAT_WINDOWS_MUSIC_IDLE_FIXTURE=1"},
			LogPath:     filepath.Join(dir, id+".log"), PendingJobsPath: jobs, StopInput: "stop\n", StopTimeout: time.Second,
			Check: func(ctx context.Context) error { return ctx.Err() },
		}
		if _, err := registry.Start(t.Context(), id, spec, func(ctx context.Context, launch func() error) error { return launch() }); err != nil {
			t.Fatal(err)
		}
		return jobs
	}
	defer m.closeWindowsMusic()
	jobs := start(windowsMusicServiceID)
	// A ready, idle owned process is reclaimed.  This is deliberately a test
	// hook duration; production scheduling obtains its timeout from the saved
	// service-retention setting through musicIdleTimeout.
	m.scheduleWindowsMusicIdle(15*time.Millisecond, 0)
	deadline := time.Now().Add(3 * time.Second)
	for registry.Status(windowsMusicServiceID).Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if registry.Status(windowsMusicServiceID).Running {
		t.Fatal("idle owned Windows music backend was not reclaimed")
	}
	jobs = start(windowsMusicServiceID)
	if err := os.WriteFile(jobs, []byte(`{"job":{"state":"accepted"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	m.scheduleWindowsMusicIdle(15*time.Millisecond, 0)
	time.Sleep(80 * time.Millisecond)
	if !registry.Status(windowsMusicServiceID).Running {
		t.Fatal("unknown receipt allowed idle reaper to stop backend")
	}
	if err := os.WriteFile(jobs, []byte(`{"job":{"state":"completed"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	// A request immediately before expiry refreshes the lease; it should not
	// be killed by the previous timer.  Re-arm at the short test duration.
	registry.Touch(windowsMusicServiceID)
	m.scheduleWindowsMusicIdle(40*time.Millisecond, 0)
	time.Sleep(20 * time.Millisecond)
	registry.Touch(windowsMusicServiceID)
	m.scheduleWindowsMusicIdle(40*time.Millisecond, 0)
	time.Sleep(25 * time.Millisecond)
	if !registry.Status(windowsMusicServiceID).Running {
		t.Fatal("adjacent request raced idle recovery")
	}
	deadline = time.Now().Add(3 * time.Second)
	for registry.Status(windowsMusicServiceID).Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if registry.Status(windowsMusicServiceID).Running {
		t.Fatal("completed backend did not reclaim after renewed lease expired")
	}
}

func TestWindowsMusicRetentionChangeInvalidatesAlreadyFiredTimer(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	registry := &pluginruntime.ManagedServices{}
	m := &Manager{managedServices: registry}
	defer m.closeWindowsMusic()
	spec := pluginruntime.ManagedServiceSpec{
		OwnerPlugin: "music", Binding: "retention-test", Executable: executable,
		Arguments: []string{"-test.run=^TestWindowsMusicIdleFixture$"}, Directory: dir,
		Environment: []string{"SystemRoot=" + os.Getenv("SystemRoot"), "WECHAT_WINDOWS_MUSIC_IDLE_FIXTURE=1"},
		LogPath:     filepath.Join(dir, "service.log"), PendingJobsPath: filepath.Join(dir, "jobs.json"), StopInput: "stop\n", StopTimeout: time.Second,
		Check: func(ctx context.Context) error { return ctx.Err() },
	}
	if _, err = registry.Start(t.Context(), windowsMusicServiceID, spec, func(ctx context.Context, launch func() error) error { return launch() }); err != nil {
		t.Fatal(err)
	}
	// Capture the old callback identity, then disable retention before it can
	// reserve the process. Invoking it directly makes this deterministic rather
	// than relying on scheduler timing.
	m.scheduleWindowsMusicIdle(time.Hour, 0)
	m.musicIdleMu.Lock()
	stale := m.musicIdleGeneration
	m.musicIdleMu.Unlock()
	m.scheduleWindowsMusicIdle(0, 0)
	m.reclaimWindowsMusicIdle(stale, time.Millisecond)
	if !registry.Status(windowsMusicServiceID).Running {
		t.Fatal("stale timer reclaimed backend after retention was disabled")
	}
	// Extending retention similarly invalidates an old immediate callback.
	m.scheduleWindowsMusicIdle(time.Hour, 0)
	m.musicIdleMu.Lock()
	stale = m.musicIdleGeneration
	m.musicIdleMu.Unlock()
	m.scheduleWindowsMusicIdle(time.Hour, 0)
	m.reclaimWindowsMusicIdle(stale, time.Millisecond)
	if !registry.Status(windowsMusicServiceID).Running {
		t.Fatal("stale timer reclaimed backend after retention was extended")
	}
}
