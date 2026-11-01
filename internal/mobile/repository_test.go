package mobile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type memoryRepository struct {
	mu          sync.Mutex
	snapshot    RepositorySnapshot
	imports     []LegacyImport
	commitError error
}

func (r *memoryRepository) Load(context.Context) (RepositorySnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return clone(r.snapshot), nil
}
func (r *memoryRepository) Initialize(_ context.Context, in LegacyImport) (RepositorySnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snapshot.Revision != 0 {
		return RepositorySnapshot{}, ErrRepositoryConflict
	}
	r.imports = append(r.imports, clone(in))
	r.snapshot = RepositorySnapshot{Settings: clone(in.Settings), Tasks: clone(in.Tasks), Revision: 1, Initialized: true}
	return clone(r.snapshot), nil
}
func (r *memoryRepository) Commit(_ context.Context, expected int64, next RepositorySnapshot) (RepositorySnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.commitError != nil {
		return RepositorySnapshot{}, r.commitError
	}
	if expected != r.snapshot.Revision {
		return RepositorySnapshot{}, ErrRepositoryConflict
	}
	next.Revision, next.Initialized = expected+1, true
	r.snapshot = clone(next)
	return clone(r.snapshot), nil
}

func validRepositoryTask(id, state string) Task {
	return Task{ID: id, RequestHash: "request-" + id, DeviceID: "lab", State: state, Source: Source{Admin: true, MessageID: id}, Created: time.Now().UTC(), Updated: time.Now().UTC(), Events: []Event{}}
}

func TestOpenWithRepositoryEmptyDirectoryDoesNotCreateLegacyFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")
	repo := &memoryRepository{}
	c, err := OpenWithRepository(dir, nil, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = os.Stat(dir); !errors.Is(err, os.ErrNotExist) || len(repo.imports) != 0 || c.repoRevision != 0 {
		t.Fatalf("empty repository unexpectedly migrated/wrote: stat=%v imports=%d revision=%d", err, len(repo.imports), c.repoRevision)
	}
}

func TestOpenWithRepositoryImportsOnceAndIgnoresLaterLegacyEdits(t *testing.T) {
	dir := t.TempDir()
	settings := Defaults()
	settings.Enabled, settings.Devices, settings.Actions = true, []string{"lab"}, []string{"tap"}
	task := validRepositoryTask("device-import", "queued")
	if err := write(filepath.Join(dir, "settings.json"), settings); err != nil {
		t.Fatal(err)
	}
	if err := write(filepath.Join(dir, "tasks.json"), map[string]Task{task.ID: task}); err != nil {
		t.Fatal(err)
	}
	repo := &memoryRepository{}
	c, err := OpenWithRepository(dir, nil, repo)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get(task.ID)
	if !ok || got.State != "needs_review" || got.Reason != "device_worker_interrupted_reobserve_required" || len(repo.imports) != 1 || repo.imports[0].SourceHash == "" {
		t.Fatalf("migration result = %#v imports=%#v", got, repo.imports)
	}
	_ = c.Close()
	// This must have no effect: active host state is authoritative after the
	// initial import and old JSON is never read again.
	settings.Devices = []string{"changed"}
	if err = write(filepath.Join(dir, "settings.json"), settings); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWithRepository(dir, nil, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, ok = reopened.Get(task.ID); !ok || got.RequestHash != task.RequestHash || reopened.Settings().Devices[0] != "lab" || len(repo.imports) != 1 {
		t.Fatalf("legacy edit overrode active host state: task=%#v settings=%#v imports=%d", got, reopened.Settings(), len(repo.imports))
	}
}

func TestOpenWithRepositoryRejectsIncompleteLegacyWithoutActivating(t *testing.T) {
	dir := t.TempDir()
	if err := write(filepath.Join(dir, "tasks.json"), map[string]Task{"bad": {ID: "bad", DeviceID: "lab"}}); err != nil {
		t.Fatal(err)
	}
	repo := &memoryRepository{}
	if _, err := OpenWithRepository(dir, nil, repo); err == nil || len(repo.imports) != 0 || repo.snapshot.Revision != 0 {
		t.Fatalf("incomplete legacy state activated: err=%v imports=%d snapshot=%#v", err, len(repo.imports), repo.snapshot)
	}
}

func TestOpenWithRepositoryRejectsDuplicateLegacyRequestHash(t *testing.T) {
	dir := t.TempDir()
	first, second := validRepositoryTask("device-one", "completed"), validRepositoryTask("device-two", "completed")
	second.RequestHash = first.RequestHash
	if err := write(filepath.Join(dir, "tasks.json"), map[string]Task{first.ID: first, second.ID: second}); err != nil {
		t.Fatal(err)
	}
	repo := &memoryRepository{}
	if _, err := OpenWithRepository(dir, nil, repo); err == nil || len(repo.imports) != 0 || repo.snapshot.Revision != 0 {
		t.Fatalf("duplicate request hash activated migration: err=%v imports=%d snapshot=%#v", err, len(repo.imports), repo.snapshot)
	}
}

func TestLegacyImportRejectsSymlinkAndSourceChange(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "actual-settings.json")
		if err := os.WriteFile(target, []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "settings.json")); err != nil {
			t.Skipf("symlink fixture unavailable: %v", err)
		}
		repo := &memoryRepository{}
		if _, err := OpenWithRepository(dir, nil, repo); err == nil || len(repo.imports) != 0 {
			t.Fatalf("symlink source activated migration: %v", err)
		}
	})

}

func TestRepositoryCommitFailureRollsBackSettingsAndTasks(t *testing.T) {
	settings := Defaults()
	settings.Enabled, settings.Devices, settings.Actions = true, []string{"lab"}, []string{"tap"}
	task := validRepositoryTask("device-running", "running")
	repo := &memoryRepository{snapshot: RepositorySnapshot{Settings: settings, Tasks: map[string]Task{task.ID: task}, Revision: 4, Initialized: true}}
	c, err := OpenWithRepository(t.TempDir(), nil, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	before := c.Settings()
	next := before
	next.Enabled = false
	repo.commitError = ErrRepositoryConflict
	if err = c.Save(next); !errors.Is(err, ErrRepositoryConflict) || c.Settings().Enabled != before.Enabled {
		t.Fatalf("Save changed memory after failed CAS: err=%v before=%#v after=%#v", err, before, c.Settings())
	}
	got, _ := c.Get(task.ID)
	if got.State != "needs_review" { // startup recovery was the only accepted change
		t.Fatalf("failed Save mutated task: %#v", got)
	}
	queued := validRepositoryTask("device-queued", "queued")
	c.mu.Lock()
	c.tasks[queued.ID] = queued
	c.mu.Unlock()
	if err = c.StopAll(); !errors.Is(err, ErrRepositoryConflict) {
		t.Fatalf("StopAll error = %v", err)
	}
	got, _ = c.Get(queued.ID)
	if got.State != "queued" {
		t.Fatalf("failed StopAll mutated in-memory task: %#v", got)
	}
}

func TestStopAllStorageFailureHaltsRunningAndQueuedTasks(t *testing.T) {
	settings := Defaults()
	settings.Enabled, settings.Devices, settings.Actions = true, []string{"lab"}, []string{"tap"}
	repo := &memoryRepository{snapshot: RepositorySnapshot{Settings: settings, Tasks: map[string]Task{}, Revision: 1, Initialized: true}}
	entered := make(chan struct{})
	secondRan := make(chan struct{}, 1)
	c, err := OpenWithRepository(t.TempDir(), func(ctx context.Context, task Task, _ Observation) (Step, error) {
		if task.Goal == "first" {
			close(entered)
			<-ctx.Done()
			return Step{}, ctx.Err()
		}
		select {
		case secondRan <- struct{}{}:
		default:
		}
		return Step{Action: "finish"}, nil
	}, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	bridge := &fakeBridge{}
	if err = c.SetBrokerFactory(func(context.Context, Task, Settings) (Bridge, error) { return bridge, nil }); err != nil {
		t.Fatal(err)
	}
	first, err := c.Start(Source{Admin: true, MessageID: "halt-first"}, "lab", "first")
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	second, err := c.Start(Source{Admin: true, MessageID: "halt-second"}, "lab", "second")
	if err != nil {
		t.Fatal(err)
	}
	repo.commitError = ErrRepositoryConflict
	if err = c.StopAll(); !errors.Is(err, ErrRepositoryConflict) {
		t.Fatalf("failed StopAll = %v", err)
	}
	if _, err = c.Start(Source{Admin: true, MessageID: "halt-new"}, "lab", "new"); !errors.Is(err, errStorageBlocked) {
		t.Fatalf("storage-blocked Start = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for c.Busy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.Busy() {
		t.Fatal("failed StopAll left worker running")
	}
	select {
	case <-secondRan:
		t.Fatal("queued task ran after failed StopAll")
	default:
	}
	if got, _ := c.Get(first.ID); got.State != "running" {
		t.Fatalf("failed StopAll did not retain inspectable task record: %#v", got)
	}
	if got, _ := c.Get(second.ID); got.State != "queued" {
		t.Fatalf("failed StopAll changed queued record: %#v", got)
	}
	repo.commitError = nil
	if err = c.StopAll(); err != nil {
		t.Fatalf("StopAll retry = %v", err)
	}
	if got, _ := c.Get(first.ID); got.State != "cancelled" {
		t.Fatalf("retry did not durably cancel first: %#v", got)
	}
	if got, _ := c.Get(second.ID); got.State != "cancelled" {
		t.Fatalf("retry did not durably cancel queued task: %#v", got)
	}
	select {
	case <-secondRan:
		t.Fatal("successful StopAll retry replayed queued task")
	default:
	}
}
