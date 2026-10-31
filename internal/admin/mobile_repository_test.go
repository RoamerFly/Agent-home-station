package admin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/mobile"
	"wechat-bot/internal/pluginhost"
)

func mobileRepositoryFixture(t *testing.T) (*pluginhost.Store, string, string) {
	t.Helper()
	root := t.TempDir()
	dbDir := filepath.Join(root, "plugin-host")
	store, err := pluginhost.OpenStore(dbDir, "synthetic-account")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, filepath.Join(root, "mobile"), filepath.Join(dbDir, "plugins.sqlite")
}

func TestMobileRepositoryFreshSaveHasOneAuthority(t *testing.T) {
	store, dir, _ := mobileRepositoryFixture(t)
	control, err := mobile.OpenWithRepository(dir, nil, &mobileRepository{store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if _, err = os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("empty open created legacy directory", err)
	}
	settings := control.Settings()
	settings.MaxSteps = 13
	if err = control.Save(settings); err != nil {
		t.Fatal(err)
	}
	state, err := store.ReadHostState(t.Context(), mobileDevicePluginID, mobileStateNamespace)
	if err != nil || state.Revision != 1 || len(state.Records) != 2 {
		t.Fatal("first save did not atomically initialize host state", state.Revision, err)
	}
	if _, err = os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("save created competing JSON directory", err)
	}
	reopened, err := mobile.OpenWithRepository(dir, nil, &mobileRepository{store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Settings().MaxSteps != 13 {
		t.Fatal("host state was not restored")
	}
}

func TestMobileRepositoryImportsStableIdentityAndNeverReplays(t *testing.T) {
	store, dir, _ := mobileRepositoryFixture(t)
	id := strings.Repeat("a", 128) // the full accepted legacy ID range fits record keys
	task := mobile.Task{ID: id, RequestHash: strings.Repeat("b", 64), State: "running", DeviceID: "phone", Goal: "synthetic inspect", Source: mobile.Source{Account: "synthetic-account", Thread: "room", Sender: "owner", MessageID: "m1", Admin: true}, Created: time.Unix(1, 0), Updated: time.Unix(2, 0), Events: []mobile.Event{{State: "running", Summary: "synthetic checkpoint"}}}
	tasks, _ := json.Marshal(map[string]mobile.Task{id: task})
	settings := validMobileSettings(t)
	writeLegacyMobileState(t, dir, settings, tasks)
	control, err := mobile.OpenWithRepository(dir, nil, &mobileRepository{store: store})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := control.Get(id)
	if !ok || got.ID != task.ID || got.RequestHash != task.RequestHash || got.Source != task.Source || got.State != "needs_review" || control.Busy() {
		t.Fatal("identity lost or unknown operation replayed", got)
	}
	if err = control.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "tasks.json"))
	if string(after) != string(tasks) {
		t.Fatal("legacy source changed during import")
	}
	// Once active, even a broken old source cannot reset or override state.
	if err = os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(`{"broken":`), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := mobile.OpenWithRepository(dir, nil, &mobileRepository{store: store})
	if err != nil {
		t.Fatal("active state fell back to old JSON", err)
	}
	defer reopened.Close()
	got, ok = reopened.Get(id)
	if !ok || got.Source != task.Source || got.State != "needs_review" {
		t.Fatal("active identity changed after legacy mutation", got)
	}
}

func TestMobileRepositoryCASAndWriteFailureKeepOriginalMemory(t *testing.T) {
	store, dir, _ := mobileRepositoryFixture(t)
	first, err := mobile.OpenWithRepository(dir, nil, &mobileRepository{store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err = first.Save(first.Settings()); err != nil {
		t.Fatal(err)
	}
	second, err := mobile.OpenWithRepository(dir, nil, &mobileRepository{store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	next := first.Settings()
	next.MaxSteps = 14
	if err = first.Save(next); err != nil {
		t.Fatal(err)
	}
	stale := second.Settings()
	stale.MaxSteps = 15
	if err = second.Save(stale); !errors.Is(err, mobile.ErrRepositoryConflict) {
		t.Fatal("stale save was not rejected", err)
	}
	if second.Settings().MaxSteps != mobile.Defaults().MaxSteps {
		t.Fatal("failed CAS changed memory")
	}
	_ = store.Close()
	next = first.Settings()
	next.MaxSteps = 16
	if err = first.Save(next); err == nil || first.Settings().MaxSteps != 14 {
		t.Fatal("write failure changed memory", err)
	}
}

func TestMobileRepositoryOnlyWritesChangedRecords(t *testing.T) {
	store, dir, dbPath := mobileRepositoryFixture(t)
	repo := &mobileRepository{store: store}
	task := mobile.Task{ID: "device-synthetic", RequestHash: "synthetic-request", State: "completed"}
	if _, err := repo.Initialize(t.Context(), mobile.LegacyImport{Settings: mobile.Defaults(), Tasks: map[string]mobile.Task{task.ID: task}, SourceHash: strings.Repeat("c", 64), ManifestVersion: 1}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A settings save must not write this unrelated task or immutable manifest.
	if _, err = db.Exec(`CREATE TRIGGER reject_unrelated_record_update BEFORE UPDATE ON host_state_records WHEN OLD.key != 'settings' BEGIN SELECT RAISE(FAIL, 'unrelated record rewritten'); END`); err != nil {
		t.Fatal(err)
	}
	control, err := mobile.OpenWithRepository(dir, nil, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	settings := control.Settings()
	settings.MaxSteps = 13
	if err = control.Save(settings); err != nil {
		t.Fatal("save rewrote unrelated records", err)
	}
}

func TestMobileRepositoryCorruptActiveStateDoesNotFallback(t *testing.T) {
	store, dir, _ := mobileRepositoryFixture(t)
	writeLegacyMobileState(t, dir, validMobileSettings(t), []byte(`{}`))
	control, err := mobile.OpenWithRepository(dir, nil, &mobileRepository{store: store})
	if err != nil {
		t.Fatal(err)
	}
	_ = control.Close()
	state, _ := store.ReadHostState(t.Context(), mobileDevicePluginID, mobileStateNamespace)
	if _, err = store.UpdateHostState(t.Context(), mobileDevicePluginID, mobileStateNamespace, state.Revision, nil, []string{"settings"}); err != nil {
		t.Fatal(err)
	}
	if _, err = mobile.OpenWithRepository(dir, nil, &mobileRepository{store: store}); err == nil {
		t.Fatal("corrupt active state silently restored old JSON")
	}
}

func TestMobileRepositoryLazyReadWithoutLegacyFiles(t *testing.T) {
	f := setup(t)
	if err := f.manager.mobile.Save(mobile.Defaults()); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.mobile.Close(); err != nil {
		t.Fatal(err)
	}
	f.manager.mobile = f.manager.newMobileService()
	defer f.manager.mobile.Close()
	if err := f.manager.mobile.InitializationError(); err != nil {
		t.Fatal(err)
	}
	if f.manager.mobile.Settings().Revision != 1 {
		t.Fatal("host-only state was ignored by cold service")
	}
}

func TestMobileRepositoryRejectsAnotherAccountBeforeActivation(t *testing.T) {
	store, dir, _ := mobileRepositoryFixture(t)
	task := mobile.Task{ID: "device-foreign", RequestHash: "synthetic-request", Source: mobile.Source{Account: "another-account"}, State: "completed"}
	tasks, _ := json.Marshal(map[string]mobile.Task{task.ID: task})
	writeLegacyMobileState(t, dir, validMobileSettings(t), tasks)
	if _, err := mobile.OpenWithRepository(dir, nil, &mobileRepository{store: store}); err == nil {
		t.Fatal("foreign account task was imported")
	}
	state, err := store.ReadHostState(t.Context(), mobileDevicePluginID, mobileStateNamespace)
	if err != nil || state.Revision != 0 {
		t.Fatal("rejected import left active namespace", state.Revision, err)
	}
}
