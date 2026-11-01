package admin

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	mobile "wechat-bot/internal/devicespec"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func installMobilePackage(t *testing.T, f *webFixture) {
	t.Helper()
	installMobilePackageForManager(t, f.manager)
}

func installMobilePackageForManager(t *testing.T, manager *Manager) pluginhost.InstalledPackage {
	t.Helper()
	store, err := manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Prepare(t.Context(), "../../plugins/mobile-control")
	if err != nil {
		t.Fatal(err)
	}
	i, err := store.Install(t.Context(), p.Token, p.Digest, p.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	i, err = store.SetEnabled(t.Context(), i.Package.ID, i.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	return i
}
func TestMobilePluginHostActionsAreProtectedAndSafe(t *testing.T) {
	f := setup(t)
	path := "/api/plugin-packages/mobile-control/actions/settings.save"
	if code, _, _ := f.request("POST", path, map[string]any{}, true, false); code != 401 {
		t.Fatal(code)
	}
	f.login()
	installMobilePackage(t, f)
	if code, _, _ := f.request("POST", path, map[string]any{}, true, false); code != 403 {
		t.Fatal(code)
	}
	code, body, _ := f.request("GET", "/api/plugin-packages/mobile-control", nil, false, false)
	if code != 200 {
		t.Fatal(code, string(body))
	}
	var detail struct {
		HostData struct {
			Settings mobilePluginSettingsView `json:"settings"`
		} `json:"host_data"`
	}
	if json.Unmarshal(body, &detail) != nil {
		t.Fatal("detail")
	}
	if detail.HostData.Settings.ConnectionProfileStatus != "待登记" {
		t.Fatal("default profile reported registered")
	}
	bad := map[string]any{"revision": detail.HostData.Settings.Revision, "enabled": false, "devices": []string{}, "actions": []string{}, "max_steps": 12, "max_seconds": 300, "bridge": map[string]any{"env": map[string]string{"X": "bad"}}}
	if code, _, _ := f.request("POST", path, bad, true, true); code != 400 {
		t.Fatal("bridge accepted", code)
	}
	good := map[string]any{"revision": detail.HostData.Settings.Revision, "enabled": false, "devices": []string{}, "actions": []string{}, "max_steps": 12, "max_seconds": 300}
	if code, _, _ := f.request("POST", path, good, true, true); code != 200 {
		t.Fatal(code)
	}
	if code, _, _ := f.request("POST", path, good, true, true); code != 409 {
		t.Fatal("stale", code)
	}
}

const mobileHostDetailPath = "/api/plugin-packages/mobile-control"

func mobileHostActionPath(action string) string { return mobileHostDetailPath + "/actions/" + action }

// These fixtures initialize the production repository and lazy Control. No
// worker is started: interrupted tasks have already been checkpointed for review.
func seedMobileHostState(t *testing.T, manager *Manager, tasks ...mobile.Task) {
	t.Helper()
	store, err := manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]mobile.Task, len(tasks))
	for _, task := range tasks {
		values[task.ID] = task
	}
	repo := &mobileRepository{store: store}
	if _, err := repo.Initialize(t.Context(), mobile.LegacyImport{Settings: mobile.Defaults(), Tasks: values, SourceHash: strings.Repeat("a", 64), ManifestVersion: 1}); err != nil {
		t.Fatal(err)
	}
}

func syntheticMobileHostTask(account, id, state string) mobile.Task {
	return mobile.Task{ID: id, RequestHash: "synthetic-request-" + id, DeviceID: "lab", Goal: "synthetic inspect " + account, State: state, Source: mobile.Source{Account: account, Thread: "synthetic-room", Sender: "synthetic-revoked-owner", Admin: true}}
}

// There is no public uninstall API yet. Inject only the installation lifecycle
// state in the fixture's real SQLite store; retained host state stays untouched.
func removeMobilePackageRegistration(t *testing.T, manager *Manager, tombstone bool) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(manager.dir, "plugin-host", "plugins.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	query := `DELETE FROM installed WHERE id=?`
	if tombstone {
		query = `UPDATE installed SET removed=1,enabled=0,revision=revision+1 WHERE id=?`
	}
	result, err := db.ExecContext(t.Context(), query, mobileDevicePluginID)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatal("installation fixture was not changed", n, err)
	}
}

func TestMobilePluginHostArchiveRetainsTasksAndOnlyAllowsStopping(t *testing.T) {
	for _, tombstone := range []bool{false, true} {
		name := "uninstalled"
		if tombstone {
			name = "removed"
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			f.login()
			installMobilePackage(t, f)
			pending := syntheticMobileHostTask("test", "pending", "needs_review")
			completed := syntheticMobileHostTask("test", "complete", "completed")
			seedMobileHostState(t, f.manager, pending, completed)
			removeMobilePackageRegistration(t, f.manager, tombstone)
			code, body, _ := f.request("GET", mobileHostDetailPath, nil, false, false)
			var detail struct {
				Archive  bool                `json:"archive"`
				UISchema pluginhost.UISchema `json:"ui_schema"`
				HostData struct {
					State       string          `json:"state"`
					Tasks       []mobile.Task   `json:"tasks"`
					Settings    json.RawMessage `json:"settings"`
					Resources   map[string]any  `json:"resources"`
					Diagnostics map[string]any  `json:"diagnostics"`
				} `json:"host_data"`
			}
			if code != 200 || json.Unmarshal(body, &detail) != nil || !detail.Archive || detail.HostData.State != "archive" || len(detail.HostData.Tasks) != 2 || len(detail.HostData.Settings) != 0 || detail.HostData.Resources == nil || detail.HostData.Diagnostics["planner_available"] != false {
				t.Fatalf("retained history unavailable: %d %s", code, body)
			}
			raw, _ := json.Marshal(detail.UISchema)
			if _, err := pluginhost.ParseUISchema(raw); err != nil || len(detail.UISchema.Sections) != 3 {
				t.Fatal("archive schema invalid", string(raw), err)
			}
			for _, section := range detail.UISchema.Sections {
				if section.Component == "configuration" || section.Action == "tasks.create" || section.Action == "settings.save" || section.Action == "devices.list" {
					t.Fatal("archive exposed new execution or configuration", section)
				}
			}
			before := f.manager.mobile.Settings()
			for _, action := range []string{"settings.save", "tasks.create", "devices.list"} {
				if code, _, _ := f.request("POST", mobileHostActionPath(action), map[string]any{}, true, true); code != 409 {
					t.Fatal("archive accepted", action, code)
				}
			}
			if code, _, _ := f.request("POST", mobileHostDetailPath+"/configuration", map[string]any{"revision": 0, "values": map[string]any{}}, true, true); code != 409 {
				t.Fatal("archive package configuration writable", code)
			}
			if !reflect.DeepEqual(before, f.manager.mobile.Settings()) {
				t.Fatal("rejected archive settings changed profile")
			}
			f.manager.mu.Lock()
			f.manager.drainTicket = "synthetic-drain"
			f.manager.mu.Unlock()
			if code, _, _ := f.request("POST", mobileHostActionPath("tasks.stop"), map[string]any{}, true, true); code != 200 {
				t.Fatal("archive stop unavailable", code)
			}
			// StopAll does not reinterpret an uncertain interrupted operation or
			// rewrite a completed task as cancelled.
			if got, _ := f.manager.mobile.Get(pending.ID); got.State != "needs_review" {
				t.Fatal("stop changed interrupted task semantics", got)
			}
			if code, _, _ := f.request("POST", mobileHostActionPath("tasks.cancel"), map[string]string{"id": pending.ID}, true, true); code != 200 {
				t.Fatal("management could not cancel revoked owner's archived task", code)
			}
			if got, _ := f.manager.mobile.Get(pending.ID); got.State != "cancelled" || got.Reason != "device_cancelled_by_owner" || got.Source != pending.Source {
				t.Fatal("archive cancellation lost task identity", got)
			}
			for _, id := range []string{pending.ID, completed.ID} {
				if code, _, _ := f.request("POST", mobileHostActionPath("tasks.cancel"), map[string]string{"id": id}, true, true); code != 200 {
					t.Fatal("terminal cancellation not idempotent", id, code)
				}
			}
			if got, _ := f.manager.mobile.Get(completed.ID); got.State != "completed" {
				t.Fatal("cancel rewrote completed task", got)
			}
			if code, _, _ := f.request("POST", mobileHostActionPath("tasks.cancel"), map[string]string{"id": "unknown"}, true, true); code != 404 {
				t.Fatal("unknown archive task accepted", code)
			}
		})
	}
}

func TestMobilePluginHostNoHistoryIsNotAnArchive(t *testing.T) {
	for _, mode := range []string{"missing", "removed", "uninstalled"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			f.login()
			if mode != "missing" {
				installMobilePackage(t, f)
				removeMobilePackageRegistration(t, f.manager, mode == "removed")
			}
			if code, body, _ := f.request("GET", mobileHostDetailPath, nil, false, false); code != 404 {
				t.Fatalf("empty state was advertised as retained history: %d %s", code, body)
			}
		})
	}
}

func TestMobilePluginHostSettingsPreservePrivateProfileAndRejectInvalidWrites(t *testing.T) {
	f := setup(t)
	f.login()
	installMobilePackage(t, f)
	settings := f.manager.mobile.Settings()
	settings.Bridge.Command = "synthetic-private-command"
	settings.Bridge.Args = []string{"--config", filepath.Join(f.dir, "synthetic-private-config"), "--storage", filepath.Join(f.dir, "synthetic-private-storage"), "synthetic-private-argument"}
	settings.Bridge.Env = map[string]string{"SYNTHETIC_API_KEY": "synthetic-private-env-key"}
	settings.Bridge.Headers = map[string]string{"Authorization": "synthetic-private-header-key"}
	settings.Bridge.URL = "https://synthetic-private-url.invalid/path"
	if err := f.manager.mobile.Save(settings); err != nil {
		t.Fatal(err)
	}
	before := f.manager.mobile.Settings()
	code, body, _ := f.request("GET", mobileHostDetailPath, nil, false, false)
	var detail struct {
		HostData struct {
			Settings mobilePluginSettingsView `json:"settings"`
		} `json:"host_data"`
	}
	if code != 200 || json.Unmarshal(body, &detail) != nil || detail.HostData.Settings.ConnectionProfileStatus != "已登记" {
		t.Fatalf("safe registered profile missing: %d %s", code, body)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	assertNoMobilePrivateKeys(t, raw["host_data"])
	for _, secret := range []string{settings.Bridge.Command, f.dir, "synthetic-private-argument", "synthetic-private-env-key", "synthetic-private-header-key", settings.Bridge.URL} {
		if strings.Contains(string(body), secret) {
			t.Fatal("package detail exposed private profile", secret)
		}
	}
	for _, field := range []string{"bridge", "env", "command", "args", "headers", "path", "api_key"} {
		t.Run("unknown_"+field, func(t *testing.T) {
			input := map[string]any{"revision": before.Revision, "enabled": true, "devices": []string{"lab"}, "actions": []string{"tap"}, "max_steps": 14, "max_seconds": 300, field: map[string]string{"SYNTHETIC": "injected"}}
			if code, _, _ := f.request("POST", mobileHostActionPath("settings.save"), input, true, true); code != 400 {
				t.Fatal("unknown private field accepted", field, code)
			}
			if !reflect.DeepEqual(before, f.manager.mobile.Settings()) {
				t.Fatal("rejected private field modified profile")
			}
		})
	}
	input := mobilePluginSettings(before)
	input.MaxSteps = 15
	f.manager.mu.Lock()
	f.manager.drainTicket = "synthetic-drain"
	f.manager.mu.Unlock()
	if code, _, _ := f.request("POST", mobileHostActionPath("settings.save"), input, true, true); code != 409 || !reflect.DeepEqual(before, f.manager.mobile.Settings()) {
		t.Fatal("draining save changed settings", code)
	}
	if code, _, _ := f.request("POST", "/api/mobile/config", before, true, true); code != 409 || !reflect.DeepEqual(before, f.manager.mobile.Settings()) {
		t.Fatal("legacy settings route bypassed draining", code)
	}
	if code, _, _ := f.request("POST", mobileHostActionPath("tasks.stop"), map[string]any{}, true, true); code != 200 {
		t.Fatal("draining blocked emergency stop", code)
	}
	f.manager.mu.Lock()
	f.manager.drainTicket = ""
	f.manager.mu.Unlock()
	if code, _, _ := f.request("POST", mobileHostActionPath("settings.save"), input, true, true); code != 200 {
		t.Fatal("valid settings save failed", code)
	}
	after := f.manager.mobile.Settings()
	if after.MaxSteps != 15 || after.Revision != before.Revision+1 || !reflect.DeepEqual(before.Bridge, after.Bridge) {
		t.Fatal("safe settings save replaced private profile")
	}
	if code, _, _ := f.request("POST", mobileHostActionPath("settings.save"), input, true, true); code != 409 || !reflect.DeepEqual(after, f.manager.mobile.Settings()) {
		t.Fatal("stale save changed settings", code)
	}
	if code, _, _ := f.request("POST", mobileHostActionPath("settings.delete"), map[string]any{}, true, true); code != 404 || !reflect.DeepEqual(after, f.manager.mobile.Settings()) {
		t.Fatal("unknown host action accepted or changed settings", code)
	}
}

func assertNoMobilePrivateKeys(t *testing.T, value any) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			switch strings.ToLower(key) {
			case "bridge", "command", "args", "env", "headers", "path", "api_key", "key":
				t.Fatal("private settings key exposed", key)
			}
			assertNoMobilePrivateKeys(t, child)
		}
	case []any:
		for _, child := range value {
			assertNoMobilePrivateKeys(t, child)
		}
	}
}

type mobileHostStartGuard struct {
	mobilePlanDeviceStub
	starts int
}

func (s *mobileHostStartGuard) Start(mobile.Source, string, string) (mobile.Task, error) {
	s.starts++
	return mobile.Task{}, errors.New("synthetic_start_must_not_execute")
}

func TestMobilePluginHostCreateRequiresEnabledReadyDeviceAndNotDraining(t *testing.T) {
	for _, mode := range []string{"missing", "disabled", "runtime_unready", "deployment_unready", "tools_unregistered", "draining"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			f.login()
			guard := &mobileHostStartGuard{mobilePlanDeviceStub: mobilePlanDeviceStub{settings: mobile.Defaults()}}
			f.manager.mobile = guard
			if mode != "missing" {
				i := installMobilePackageForManager(t, f.manager)
				store, _ := f.manager.packageStore()
				if mode == "disabled" {
					if _, err := store.SetEnabled(t.Context(), i.Package.ID, i.Revision, false); err != nil {
						t.Fatal(err)
					}
				}
				controller, err := pluginruntime.NewController(store, nil)
				if err != nil {
					t.Fatal(err)
				}
				f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, mode != "runtime_unready"
			}
			options := pluginruntime.Options{}
			if mode == "tools_unregistered" || mode == "draining" {
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				options.Python, options.CatalogDirectory = executable, t.TempDir()
			}
			if mode == "draining" {
				options, guard.settings = mobileDeviceIOResourceFixture(t, f.manager, guard.settings, "")
			}
			raw, _ := json.Marshal(options)
			t.Setenv("WECHAT_PLUGIN_RUNTIME_CONFIG", string(raw))
			if mode == "draining" {
				if err := f.manager.mobileDeviceReady(t.Context()); err != nil {
					t.Fatal("drain fixture must otherwise be ready", err)
				}
				f.manager.mu.Lock()
				f.manager.drainTicket = "synthetic-drain"
				f.manager.mu.Unlock()
			}
			if code, body, _ := f.request("POST", mobileHostActionPath("tasks.create"), map[string]string{"request_key": "synthetic-create", "device_id": "lab", "goal": "view synthetic page"}, true, true); code != 409 || guard.starts != 0 {
				t.Fatalf("rejected task reached device service: %d starts=%d %s", code, guard.starts, body)
			}
		})
	}
}

func TestMobilePluginHostActionsAreAccountScoped(t *testing.T) {
	f, accounts := setupAccounts(t)
	second := accounts.lookup("second").manager
	for _, manager := range []*Manager{f.manager, second} {
		installMobilePackageForManager(t, manager)
		seedMobileHostState(t, manager, syntheticMobileHostTask(manager.native.Account(), "same-task", "needs_review"))
	}
	mainBefore := f.manager.mobile.Settings()
	for _, action := range []string{"tasks.cancel", "tasks.stop", "settings.save", "tasks.create"} {
		if code, _, _ := f.request("POST", mobileHostActionPath(action)+"?account=missing", map[string]any{"id": "same-task"}, true, true); code != 404 {
			t.Fatal("unknown account fell back to current mobile state", action, code)
		}
	}
	code, body, response := f.request("GET", mobileHostDetailPath+"?account=second", nil, false, false)
	if code != 200 || response.Header.Get("X-Wechat-Account") != "second" || strings.Contains(string(body), "synthetic inspect test") || !strings.Contains(string(body), "synthetic inspect second") {
		t.Fatalf("wrong account detail: %d %s", code, body)
	}
	if code, _, _ := f.request("POST", mobileHostActionPath("tasks.cancel")+"?account=second", map[string]string{"id": "same-task"}, true, true); code != 200 {
		t.Fatal(code)
	}
	if task, _ := second.mobile.Get("same-task"); task.State != "cancelled" {
		t.Fatal("selected account was not cancelled", task)
	}
	if task, _ := f.manager.mobile.Get("same-task"); task.State != "needs_review" {
		t.Fatal("other account cancellation affected current task", task)
	}
	if code, body, _ := f.request("POST", "/api/accounts/second/manage", map[string]string{"action": "remove"}, true, true); code != 200 {
		t.Fatalf("remove account: %d %s", code, body)
	}
	for _, action := range []string{"tasks.cancel", "tasks.stop", "settings.save", "tasks.create"} {
		if code, _, _ := f.request("POST", mobileHostActionPath(action)+"?account=second", map[string]any{"id": "same-task"}, true, true); code != 404 {
			t.Fatal("removed account routed", action, code)
		}
	}
	if code, _, _ := f.request("GET", mobileHostDetailPath+"?account=second", nil, false, false); code != 404 {
		t.Fatal("removed account detail routed", code)
	}
	if task, _ := f.manager.mobile.Get("same-task"); task.State != "needs_review" || !reflect.DeepEqual(mainBefore, f.manager.mobile.Settings()) {
		t.Fatal("wrong/removed account mutated current state", task)
	}
}

func TestMobilePluginHostResourcesRequireSessionAndSelectedAccount(t *testing.T) {
	f, accounts := setupAccounts(t)
	second := accounts.lookup("second").manager
	installMobilePackageForManager(t, second)
	task := syntheticMobileHostTask("second", "resource-task", "completed")
	task.ObservationID = "obs-" + strings.Repeat("b", 32)
	clip := "clip-" + strings.Repeat("c", 32)
	task.Artifacts = []mobile.Artifact{{ID: clip, Kind: "screenrecord", MIME: "video/mp4"}}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	// A synthetic container header verifies routing only, not playback.
	mp4Data := []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")
	task.Artifacts[0].Bytes = len(mp4Data)
	task.Artifacts[0].SHA256 = namedSpeechSHA(mp4Data)
	seedMobileHostState(t, second, task)
	for path, data := range map[string][]byte{filepath.Join(second.dir, "mobile", "observations", task.ObservationID+".png"): pngData.Bytes(), filepath.Join(second.dir, "mobile", "recordings", clip+".mp4"): mp4Data} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Retained resources stay available after uninstall, under the same account.
	removeMobilePackageRegistration(t, second, false)
	for _, resource := range []struct {
		suffix, mime string
		data         []byte
	}{{"screenshot/" + task.ObservationID, "image/png", pngData.Bytes()}, {"recording/" + clip, "video/mp4", mp4Data}} {
		path := "/api/mobile/tasks/" + task.ID + "/" + resource.suffix
		code, body, response := f.request("GET", path+"?account=second", nil, false, false)
		if code != 200 || !bytes.Equal(body, resource.data) || response.Header.Get("Content-Type") != resource.mime {
			t.Fatal("retained resource unavailable", code)
		}
		for _, account := range []string{"test", "missing"} {
			if code, _, _ := f.request("GET", path+"?account="+account, nil, false, false); code != 404 {
				t.Fatal("resource crossed account", account, code)
			}
		}
		if code, _, _ := f.request("GET", strings.Replace(path, task.ID, "wrong-task", 1)+"?account=second", nil, false, false); code != 404 {
			t.Fatal("resource crossed task", code)
		}
		if code, _, _ := f.request("GET", path+"-wrong?account=second", nil, false, false); code != 404 {
			t.Fatal("unbound resource accepted", code)
		}
		anonymous := &http.Client{}
		r, err := anonymous.Get(f.server.URL + path + "?account=second")
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 401 {
			t.Fatal("unauthenticated resource accessible", r.StatusCode)
		}
	}
	if code, _, _ := f.request("POST", "/api/accounts/second/manage", map[string]string{"action": "remove"}, true, true); code != 200 {
		t.Fatal("remove resource account", code)
	}
	for _, suffix := range []string{"screenshot/" + task.ObservationID, "recording/" + clip} {
		path := "/api/mobile/tasks/" + task.ID + "/" + suffix + "?account=second"
		if code, _, _ := f.request("GET", path, nil, false, false); code != 404 {
			t.Fatal("removed account resource routed", code)
		}
		// The same request without the shared session must fail before routing.
		r, err := (&http.Client{}).Get(f.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 401 {
			t.Fatal("unauthenticated resource accessible", r.StatusCode)
		}
	}
}
