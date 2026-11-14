package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/devicebroker"
	mobile "wechat-bot/internal/devicespec"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

// authorizedObservationRuntime is the installed mobile package running against
// a synthetic ADB endpoint.  It is deliberately not a unit mock of the
// plugin: the test reaches the same Controller, bridge and devicebroker path
// that a private-surf observation uses in production.
type authorizedObservationRuntime struct {
	service      *mobileWorkflowService
	store        *pluginhost.Store
	grant        mobileObservationAppGrant
	png          []byte
	foreground   string
	calls        int
	screenshots  int
	actions      int
	onScreenshot func()
	server       *mobileIOTestADBServer
}

func newAuthorizedObservationRuntime(t *testing.T) *authorizedObservationRuntime {
	t.Helper()
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in installed mobile runtime replay")
	}
	python, _ = filepath.Abs(python)
	f := setup(t)
	service := f.manager.mobile.(*mobileWorkflowService)
	settings := service.Settings()
	settings.Enabled = true
	settings.Devices = []string{"synthetic"}
	settings.Actions = []string{"capture"}
	options, settings := mobileDeviceIOResourceFixture(t, f.manager, settings, python)
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	options.WindowsHelper = helper
	rawOptions, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECHAT_PLUGIN_RUNTIME_CONFIG", string(rawOptions))
	if err = service.Save(settings); err != nil {
		t.Fatal(err)
	}
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(t.Context(), "../../plugins/mobile-control")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetEnabled(t.Context(), installed.Package.ID, installed.Revision, true); err != nil {
		t.Fatal(err)
	}
	if _, err = f.manager.fish.Bind(f.manager.native.Account()); err != nil {
		t.Fatal(err)
	}
	_, bindingID, err := f.manager.fish.BindDevice(f.manager.native.Account(), "synthetic")
	if err != nil || bindingID == "" {
		t.Fatalf("bind synthetic device binding=%q err=%v", bindingID, err)
	}
	if _, err = service.SetObservationAppGrant("synthetic", bindingID, "com.example.home", true); err != nil {
		t.Fatal(err)
	}
	grant, ok, err := service.ObservationAppGrant("synthetic", "com.example.home")
	if err != nil || !ok {
		t.Fatalf("read observation grant grant=%#v ok=%t err=%v", grant, ok, err)
	}
	controller, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, options))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true

	var pngData bytes.Buffer
	if err = png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 16, 12))); err != nil {
		t.Fatal(err)
	}
	runtime := &authorizedObservationRuntime{
		service: service, store: store, grant: grant, png: pngData.Bytes(), foreground: "com.example.home", server: &mobileIOTestADBServer{},
	}
	previous := openMobileDeviceBroker
	openMobileDeviceBroker = func(ctx context.Context, option devicebroker.Options) (devicebroker.IOBroker, error) {
		option.ADBServer = runtime.server
		option.Runner = func(ctx context.Context, path string, args []string, limit int64, timeout time.Duration) ([]byte, error) {
			runtime.calls++
			if path != filepath.Join(options.DeviceToolsRoots[0], "adb.exe") || len(args) < 7 || !reflect.DeepEqual(args[:6], []string{"-H", "127.0.0.1", "-P", "5037", "-s", "synthetic-serial-synthetic"}) || limit < 1 || timeout <= 0 {
				return nil, errors.New("unexpected synthetic adb invocation")
			}
			tail := args[6:]
			switch {
			case reflect.DeepEqual(tail, []string{"get-state"}):
				return []byte("device\n"), nil
			case reflect.DeepEqual(tail, []string{"shell", "dumpsys", "window"}):
				return []byte("mCurrentFocus=Window{synthetic " + runtime.foreground + "/.Main}"), nil
			case reflect.DeepEqual(tail, []string{"exec-out", "screencap", "-p"}):
				runtime.screenshots++
				if runtime.onScreenshot != nil {
					runtime.onScreenshot()
				}
				return runtime.png, nil
			case len(tail) == 4 && tail[0] == "shell" && tail[1] == "uiautomator" && tail[2] == "dump" && strings.HasPrefix(tail[3], "/sdcard/whale-ui-"):
				return nil, nil
			case len(tail) == 3 && tail[0] == "exec-out" && tail[1] == "cat" && strings.HasSuffix(tail[2], ".xml"):
				return []byte(`<hierarchy><node text="synthetic page" package="com.example.home" bounds="[0,0][16,12]"/></hierarchy>`), nil
			case len(tail) == 4 && tail[0] == "shell" && tail[1] == "rm" && tail[2] == "-f":
				return nil, nil
			default:
				runtime.actions++
				return nil, errors.New("unexpected synthetic adb operation")
			}
		}
		return devicebroker.OpenIO(ctx, option)
	}
	t.Cleanup(func() { openMobileDeviceBroker = previous })
	return runtime
}

func (r *authorizedObservationRuntime) observe(ctx context.Context) (mobileDeviceOverview, error) {
	return r.service.observeAuthorizedApp(ctx, r.grant, func(context.Context) error { return nil })
}

// privateObservationTasks reads only the host's durable task state, so a
// cancellation test can prove that a late runtime failure did not turn into a
// completed observation.  The live call deliberately does not return a task
// id on a failed observation.
func (r *authorizedObservationRuntime) privateObservationTasks(t *testing.T) []mobile.Task {
	t.Helper()
	r.service.stateMu.Lock()
	defer r.service.stateMu.Unlock()
	snapshot, _, err := r.service.load(false)
	if err != nil {
		t.Fatal(err)
	}
	items := make([]mobile.Task, 0)
	for _, task := range snapshot.tasks {
		if task.DeviceID == r.grant.DeviceID && task.Source.Account == r.grant.Account && task.Source.Thread == "host:surf:"+r.grant.Account {
			items = append(items, task)
		}
	}
	return items
}

func assertNoCompletedPrivateObservation(t *testing.T, runtime *authorizedObservationRuntime) {
	t.Helper()
	tasks := runtime.privateObservationTasks(t)
	if len(tasks) != 1 {
		t.Fatalf("private observation task count=%d tasks=%#v", len(tasks), tasks)
	}
	if tasks[0].State == "completed" || tasks[0].ObservationID != "" || tasks[0].Source.Admin {
		t.Fatalf("cancelled private observation became visible as completed task=%#v", tasks[0])
	}
}

// Keep this name short: Go's test TempDir becomes part of the installed
// Python runtime's atomically-written native ledger path on Windows.
func TestPrivatePhoneInstalled(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		runtime := newAuthorizedObservationRuntime(t)
		view, err := runtime.observe(t.Context())
		if err != nil {
			jobs, jobsErr := runtime.store.Jobs(t.Context(), mobileDevicePluginID, 0, 20)
			raw, _ := json.Marshal(jobs.Items)
			t.Fatalf("observe=%v jobs_error=%v jobs=%s", err, jobsErr, raw)
		}
		if runtime.server.acquisitions == 0 || runtime.screenshots != 1 || runtime.actions != 0 || view.CurrentApp != runtime.grant.Package || view.ObservationID == "" {
			t.Fatalf("view=%#v acquisitions=%d screenshots=%d actions=%d", view, runtime.server.acquisitions, runtime.screenshots, runtime.actions)
		}
		task, ok := runtime.service.Get(view.TaskID)
		if !ok || task.Source.Admin || task.Source.Sender != "" || task.Source.Account != runtime.grant.Account || task.Source.Thread != "host:surf:"+runtime.grant.Account || task.FishID != runtime.grant.FishID || task.DeviceBindingID != runtime.grant.BindingID {
			t.Fatalf("private observation task=%#v found=%t grant=%#v", task, ok, runtime.grant)
		}
		runtime.service.stateMu.Lock()
		snapshot, _, loadErr := runtime.service.load(false)
		runtime.service.stateMu.Unlock()
		var record struct {
			State map[string]any `json:"state"`
		}
		if loadErr != nil || json.Unmarshal(snapshot.records[view.TaskID].State, &record.State) != nil || record.State["observation_grant_id"] != runtime.grant.GrantID || record.State["observation_package"] != runtime.grant.Package {
			t.Fatalf("private observation evidence missing load=%v record=%s", loadErr, snapshot.records[view.TaskID].State)
		}
		data, err := runtime.service.Screenshot(view.TaskID, view.ObservationID)
		if err != nil || !bytes.Equal(data, runtime.png) {
			t.Fatalf("stored screenshot err=%v bytes=%d", err, len(data))
		}
	})

	t.Run("wrong_app", func(t *testing.T) {
		runtime := newAuthorizedObservationRuntime(t)
		runtime.foreground = "com.browser"
		if _, err := runtime.observe(t.Context()); err == nil || err.Error() != "device_observation_app_mismatch" {
			t.Fatalf("wrong foreground err=%v", err)
		}
		if runtime.screenshots != 0 || runtime.actions != 0 {
			t.Fatalf("wrong foreground performed protected read screenshots=%d actions=%d", runtime.screenshots, runtime.actions)
		}
	})

	t.Run("revoke", func(t *testing.T) {
		runtime := newAuthorizedObservationRuntime(t)
		revoked := false
		runtime.onScreenshot = func() {
			if revoked {
				return
			}
			revoked = true
			if _, err := runtime.service.SetObservationAppGrant(runtime.grant.DeviceID, runtime.grant.BindingID, runtime.grant.Package, false); err != nil {
				t.Errorf("revoke during screenshot: %v", err)
			}
		}
		if _, err := runtime.observe(t.Context()); err == nil {
			t.Fatal("revoked observation unexpectedly completed")
		}
		if !revoked || runtime.screenshots != 1 || runtime.actions != 0 {
			t.Fatalf("revoke hook=%t screenshots=%d actions=%d", revoked, runtime.screenshots, runtime.actions)
		}
		if _, ok, err := runtime.service.ObservationAppGrant(runtime.grant.DeviceID, runtime.grant.Package); err != nil || ok {
			t.Fatalf("revoked grant still current ok=%t err=%v", ok, err)
		}
		assertNoCompletedPrivateObservation(t, runtime)
	})

	t.Run("takeover", func(t *testing.T) {
		runtime := newAuthorizedObservationRuntime(t)
		paused := false
		runtime.onScreenshot = func() {
			if paused {
				return
			}
			paused = true
			if _, err := runtime.service.SetObservationPaused(runtime.grant.DeviceID, true, true); err != nil {
				t.Errorf("take over during screenshot: %v", err)
			}
		}
		if _, err := runtime.observe(t.Context()); err == nil {
			t.Fatal("taken-over observation unexpectedly completed")
		}
		if !paused || runtime.screenshots != 1 || runtime.actions != 0 {
			t.Fatalf("takeover hook=%t screenshots=%d actions=%d", paused, runtime.screenshots, runtime.actions)
		}
		assertNoCompletedPrivateObservation(t, runtime)
	})
}
