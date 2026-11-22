package admin

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func TestRegisteredPortableMusicPythonRequiresPinnedRelease(t *testing.T) {
	release := t.TempDir()
	adminDir := filepath.Join(release, "data", "applications", "wechat", "legacy-admin")
	runtimeRoot := filepath.Join(release, "env", "windows-python-cli")
	for _, dir := range []string{adminDir, runtimeRoot} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	python := filepath.Join(runtimeRoot, "python.exe")
	original := []byte("synthetic pinned CLI Python")
	if err := os.WriteFile(python, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, "manifest.json"), []byte(`{"package":"agent-home-station","platform":"windows-amd64","layout":{"data":"data","runtimes":"env"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(original)
	runtimeManifest, _ := json.Marshal(map[string]any{"platform": "windows-x64", "files": map[string]string{"python.exe": hex.EncodeToString(digest[:])}})
	if err := os.WriteFile(filepath.Join(runtimeRoot, "runtime-manifest.json"), runtimeManifest, 0600); err != nil {
		t.Fatal(err)
	}
	if !registeredPortableMusicPython(adminDir, python) {
		t.Fatal("pinned release CLI Python rejected")
	}
	if registeredPortableMusicPython(filepath.Join(release, "data", "other-admin"), python) ||
		registeredPortableMusicPython(adminDir, filepath.Join(release, "other", "python.exe")) {
		t.Fatal("unrelated administrator or interpreter admitted")
	}
	if err := os.WriteFile(python, []byte("modified executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if registeredPortableMusicPython(adminDir, python) {
		t.Fatal("changed CLI Python admitted")
	}
}

func TestRegisteredPortableMusicPythonInstalledRelease(t *testing.T) {
	release := os.Getenv("AHS_WINDOWS_RELEASE_ROOT")
	if release == "" {
		t.Skip("set AHS_WINDOWS_RELEASE_ROOT for the installed Windows runtime check")
	}
	adminDir := filepath.Join(release, "data", "applications", "wechat", "legacy-admin")
	python := filepath.Join(release, "env", "windows-python-cli", "python.exe")
	if !registeredPortableMusicPython(adminDir, python) {
		t.Fatal("installed Windows CLI Python is not a pinned music runtime")
	}
	if candidate := os.Getenv("AHS_WINDOWS_MUSIC_CANDIDATE"); candidate != "" {
		data, err := os.ReadFile(candidate)
		if err != nil {
			t.Fatal(err)
		}
		manager := &Manager{dir: adminDir}
		config, _, err := manager.validateWindowsMusicLaunchConfig(data)
		if err != nil || config.Python != python {
			t.Fatalf("isolated music registration not ready: %v", err)
		}
	}
}

// Exercise the installed music capability against a disposable release-shaped
// data root. The real packaged interpreter and music bridge run, while the
// WeChat account, music token, IPC and ComfyUI child are isolated test state.
func TestLivePortableMusicBridgeThroughInstalledCapability(t *testing.T) {
	formal := os.Getenv("AHS_WINDOWS_RELEASE_ROOT")
	if formal == "" {
		t.Skip("set AHS_WINDOWS_RELEASE_ROOT for installed Windows music acceptance")
	}
	for _, port := range []string{"6188", "6208"} {
		listener, err := net.Listen("tcp4", "127.0.0.1:"+port)
		if err != nil {
			t.Fatalf("music acceptance port %s unavailable: %v", port, err)
		}
		listener.Close()
	}
	root := t.TempDir()
	adminDir := filepath.Join(root, "data", "applications", "wechat", "legacy-admin")
	pythonRoot := filepath.Join(root, "env", "windows-python-cli")
	if err := os.MkdirAll(adminDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(pythonRoot, os.DirFS(filepath.Join(formal, "env", "windows-python-cli"))); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(formal, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(pythonRoot, "python.exe")
	if !registeredPortableMusicPython(adminDir, python) {
		t.Fatal("copied, pinned portable Python not admitted")
	}
	formalPython := filepath.Join(formal, "env", "windows-python-cli")
	formalHelper := filepath.Join(formal, "env", "windows-agent-host")
	options, _ := json.Marshal(pluginruntime.Options{
		Python: filepath.Join(formalPython, "python.exe"), Pythonw: filepath.Join(formalPython, "pythonw.exe"),
		WindowsHelper: filepath.Join(formalHelper, "windows_plugin_host.pyw"),
		RuntimeRoots:  []string{formalPython, formalHelper}, CatalogDirectory: filepath.Join(formal, "publictools"),
	})
	t.Setenv("WECHAT_PLUGIN_RUNTIME_CONFIG", string(options))
	f, p, _ := musicServiceFixture(t)
	originalDir := f.manager.dir
	f.manager.dir = adminDir
	t.Cleanup(func() { f.manager.dir = originalDir })
	if err := os.MkdirAll(filepath.Join(f.manager.managedRoot(), "public_workspace"), 0700); err != nil {
		t.Fatal(err)
	}
	registration, err := os.ReadFile(filepath.Join(formal, "data", "tools", "yue", "station-yue.json"))
	if err != nil {
		t.Fatal(err)
	}
	var comfy map[string]any
	if err := json.Unmarshal(registration, &comfy); err != nil {
		t.Fatal(err)
	}
	comfy["device"] = "cpu"
	registration, _ = json.Marshal(comfy)
	comfyFile := filepath.Join(adminDir, "acceptance-comfy.json")
	if err := os.WriteFile(comfyFile, registration, 0600); err != nil {
		t.Fatal(err)
	}
	ipc := filepath.Join(adminDir, "music-ipc")
	if err := os.Mkdir(ipc, 0700); err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(windowsMusicConfig{
		Enabled: true, Policy: windowsMusicPolicy, Python: python,
		Entry: filepath.Join(formal, "env", "windows-station-rvc", "managed_music_service.py"),
		Media: filepath.Join(formal, "env", "windows-media"), IPC: ipc,
		ComfyURL: "http://127.0.0.1:6208", ComfyRegistration: comfyFile,
	})
	if err := os.WriteFile(filepath.Join(adminDir, "windows-music.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.manager.windowsMusicLaunchConfig(); err != nil {
		t.Fatalf("portable launch registration: %v", err)
	}
	started, err := p.controlMusicService(t.Context(), "start")
	if err != nil || !strings.Contains(started, `"ready":true`) {
		log, _ := os.ReadFile(filepath.Join(adminDir, "windows-music.log"))
		t.Fatalf("portable music start: %s %v, service log: %.1200s", started, err, log)
	}
	defer func() {
		if f.manager.windowsMusicStatus().Running {
			if _, stopErr := p.controlMusicService(context.Background(), "stop"); stopErr != nil {
				t.Errorf("portable music cleanup: %v", stopErr)
			}
		}
	}()
	if health := p.probeYuE(t.Context()); !health.Ready || health.BridgeVersion < 1 {
		t.Fatalf("authenticated portable bridge not ready: %+v", health)
	}
	status, err := p.controlMusicService(t.Context(), "status")
	if err != nil || !strings.Contains(status, `"configured":true`) || !strings.Contains(status, `"running":true`) {
		t.Fatalf("portable music status: %s %v", status, err)
	}
	stopped, err := p.controlMusicService(t.Context(), "stop")
	if err != nil || !strings.Contains(stopped, `"state":"stopped"`) {
		t.Fatalf("portable music stop: %s %v", stopped, err)
	}
	for _, port := range []string{"6188", "6208"} {
		connection, dialErr := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
		if dialErr == nil {
			connection.Close()
			t.Fatalf("portable music service left port %s open", port)
		}
	}
	t.Log("installed capability started the pinned portable Python, authenticated the real bridge, reported ready, and stopped both isolated services; no generation or WeChat send")
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
	save()
	if !f.manager.windowsMusicRegistered() {
		t.Fatal("complete registration was rejected")
	}
	config.Python = filepath.Join(public, "runtimes", "moved", "python.exe")
	save()
	if _, err := f.manager.startWindowsMusic(t.Context()); f.manager.windowsMusicRegistered() || err == nil || err.Error() != "windows_music_python_unavailable" {
		t.Fatal("missing interpreter was reported as configured", err)
	}
	config.Python = python
	config.ComfyURL = "http://127.0.0.1:6193"
	config.ComfyRegistration = filepath.Join(control, "moved", "station-registration.json")
	save()
	if _, err := f.manager.startWindowsMusic(t.Context()); f.manager.windowsMusicRegistered() || err == nil || err.Error() != "windows_music_comfy_registration_invalid" {
		t.Fatal("missing ComfyUI registration was advertised as configured", err)
	}
	config.ComfyURL, config.ComfyRegistration = "", ""
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
	if _, err := f.manager.startWindowsMusic(t.Context()); err == nil {
		t.Fatal("synthetic executable unexpectedly started")
	}
	selected, _, err := f.manager.localMusicPluginConnection(t.Context())
	if err != nil || selected == "http://"+listener.Addr().String() {
		t.Fatal("foreign listener was selected for music", selected, err)
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

func TestRegisteredComfyURLOnlyAllowsLoopback(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"empty", "", ""},
		{"canonical", "http://127.0.0.1:6191", "http://127.0.0.1:6191"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := registeredComfyURL(tc.raw)
			if err != nil || got != tc.want {
				t.Fatalf("registeredComfyURL(%q) = %q, %v", tc.raw, got, err)
			}
		})
	}
	for _, raw := range []string{
		"http://localhost:6191",
		"http://192.168.1.2:6191",
		"https://127.0.0.1:6191",
		"http://127.0.0.1:80",
		"http://127.0.0.1:6191/path",
	} {
		if _, err := registeredComfyURL(raw); err == nil {
			t.Fatalf("registeredComfyURL accepted unsafe endpoint %q", raw)
		}
	}
}

func TestRegisteredComfyInputRequiresCanonicalAbsolutePath(t *testing.T) {
	if got, err := registeredComfyInput(""); err != nil || got != "" {
		t.Fatalf("empty input = %q, %v", got, err)
	}
	for _, raw := range []string{"relative/input", "C:/Comfy/input/../input"} {
		if _, err := registeredComfyInput(raw); err == nil {
			t.Fatalf("registeredComfyInput accepted %q", raw)
		}
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
