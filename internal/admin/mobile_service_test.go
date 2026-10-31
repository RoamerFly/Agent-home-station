package admin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"wechat-bot/internal/mobile"
	"wechat-bot/internal/pluginruntime"
)

type orderedMobileShutdown struct {
	deviceService
	check  func()
	closed bool
}

func (s *orderedMobileShutdown) Close() error {
	if !s.closed {
		s.closed = true
		s.check()
	}
	return s.deviceService.Close()
}

func TestManagerStopsDeviceTaskOwnersBeforeClosingPluginSessions(t *testing.T) {
	f := setup(t)
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(t.Context(), readySpeechFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	executor := &packageTestExecutor{}
	controller, err := pluginruntime.NewController(store, executor)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime = controller
	if err = controller.Start(t.Context(), installed.Package.ID, installed.Revision); err != nil {
		t.Fatal(err)
	}
	f.manager.mobile = &orderedMobileShutdown{deviceService: f.manager.mobile, check: func() {
		if controller.Status(installed.Package.ID).State != "running" {
			t.Error("plugin session was closed before device task cancellation could finish")
		}
	}}
	if err = f.manager.Close(); err != nil {
		t.Fatal(err)
	}
	if executor.closes != 1 {
		t.Fatal("plugin session was not reaped", executor.closes)
	}
}

func writeLegacyMobileState(t *testing.T, dir string, settings []byte, tasks []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), settings, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), tasks, 0600); err != nil {
		t.Fatal(err)
	}
}

func validMobileSettings(t *testing.T) []byte {
	t.Helper()
	value, err := json.Marshal(mobile.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestLazyMobileServiceColdReadsDoNotCreateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mobile")
	var opens atomic.Int32
	service := newLazyMobileService(dir, nil, func(string, mobile.Planner) (*mobile.Control, error) {
		opens.Add(1)
		return nil, errors.New("must_not_open")
	}, nil)
	if got := service.Settings(); got.Revision != mobile.Defaults().Revision {
		t.Fatal("cold service did not return defaults", got)
	}
	if tasks := service.List(nil); len(tasks) != 0 || service.Busy() || service.InitializationError() != nil {
		t.Fatal("cold service opened or reported a phantom error", tasks)
	}
	if err := service.StopAll(); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if opens.Load() != 0 {
		t.Fatal("cold read opened device service", opens.Load())
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cold reads created legacy directory", err)
	}
}

func TestLazyMobileServiceBrokerWiringFailureDoesNotExposeLegacyControl(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mobile")
	want := errors.New("device_broker_wiring_failed")
	service := newLazyMobileService(dir, nil, nil, func(control *mobile.Control) error { return want })
	defer service.Close()
	if err := service.Save(mobile.Defaults()); !errors.Is(err, want) {
		t.Fatal("broker wiring failure was hidden", err)
	}
	service.mu.Lock()
	control := service.control
	service.mu.Unlock()
	if control != nil || service.Busy() {
		t.Fatal("legacy control exposed after plugin broker wiring failed")
	}
}

func TestLazyMobileServicePreservesBrokenStateAndRetriesAfterRepair(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mobile")
	broken := []byte(`{"enabled":`)
	writeLegacyMobileState(t, dir, broken, []byte(`{}`))
	var opens atomic.Int32
	service := newLazyMobileService(dir, nil, func(path string, planner mobile.Planner) (*mobile.Control, error) {
		opens.Add(1)
		return mobile.Open(path, planner)
	}, nil)
	_ = service.Settings()
	if err := service.InitializationError(); err == nil {
		t.Fatal("broken configuration was hidden")
	}
	after, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil || string(after) != string(broken) {
		t.Fatal("broken configuration was overwritten", string(after), err)
	}
	if err = os.WriteFile(filepath.Join(dir, "settings.json"), validMobileSettings(t), 0600); err != nil {
		t.Fatal(err)
	}
	if err = service.InitializationError(); err != nil {
		t.Fatal("repaired configuration did not retry", err)
	}
	if opens.Load() < 2 {
		t.Fatal("repair did not cause a new open", opens.Load())
	}
}

func TestLazyMobileServiceStopAllDoesNotOpenOrMutateBrokenLegacyState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mobile")
	brokenSettings, brokenTasks := []byte(`{"enabled":`), []byte(`{"unfinished":`)
	writeLegacyMobileState(t, dir, brokenSettings, brokenTasks)
	var opens atomic.Int32
	service := newLazyMobileService(dir, nil, func(string, mobile.Planner) (*mobile.Control, error) {
		opens.Add(1)
		return nil, errors.New("must_not_open")
	}, nil)
	if err := service.StopAll(); err != nil {
		t.Fatal("stop should not initialize an unused service", err)
	}
	afterSettings, settingsErr := os.ReadFile(filepath.Join(dir, "settings.json"))
	afterTasks, tasksErr := os.ReadFile(filepath.Join(dir, "tasks.json"))
	if settingsErr != nil || tasksErr != nil || string(afterSettings) != string(brokenSettings) || string(afterTasks) != string(brokenTasks) || opens.Load() != 0 {
		t.Fatal("stop opened or changed damaged legacy state", settingsErr, tasksErr, opens.Load())
	}
}

func TestLazyMobileServiceConcurrentOpenAndCloseRaceDoesNotRevive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mobile")
	writeLegacyMobileState(t, dir, validMobileSettings(t), []byte(`{}`))
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var opens atomic.Int32
	service := newLazyMobileService(dir, nil, func(path string, planner mobile.Planner) (*mobile.Control, error) {
		opens.Add(1)
		once.Do(func() { close(started) })
		<-release
		return mobile.Open(path, planner)
	}, nil)

	first := make(chan error, 1)
	go func() { first <- service.InitializationError() }()
	<-started
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			_ = service.Settings()
		}()
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- service.Close() }()
	close(release)
	readers.Wait()
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-first; err != nil {
		t.Fatal("initial open failed", err)
	}
	if opens.Load() != 1 {
		t.Fatal("concurrent readers opened more than once", opens.Load())
	}
	service.mu.Lock()
	control := service.control
	service.mu.Unlock()
	if control != nil || service.Busy() {
		t.Fatal("closed service was revived", control)
	}
	if _, err := service.Start(mobile.Source{Admin: true, MessageID: "synthetic"}, "device", "goal"); !errors.Is(err, errDeviceServiceClosed) || opens.Load() != 1 {
		t.Fatal("closed service reopened", err, opens.Load())
	}
}
