package mobile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

// Repository stores only the durable device settings and task records. Media
// observations, recordings and their private ledger stay in Control's host
// private directory and are deliberately not copied during migration.
// Revision is an adapter-owned CAS value; zero means no active host state yet.
type RepositorySnapshot struct {
	Settings    Settings        `json:"settings"`
	Tasks       map[string]Task `json:"tasks"`
	Revision    int64           `json:"revision"`
	Initialized bool            `json:"initialized"`
}

// LegacyImport is validated by mobile before it reaches the host adapter.
// SourcePath intentionally is not exposed: callers receive only the stable
// content hash and records required for an atomic one-time import.
type LegacyImport struct {
	Settings        Settings        `json:"settings"`
	Tasks           map[string]Task `json:"tasks"`
	SourceHash      string          `json:"source_hash"`
	ManifestVersion int             `json:"manifest_version"`
}

// Repository is implemented by the host-state adapter. Commit must apply the
// settings/task diff atomically with expectedRevision CAS and return the exact
// committed revision. It must not copy media files or silently merge conflicts.
type Repository interface {
	Load(context.Context) (RepositorySnapshot, error)
	Initialize(context.Context, LegacyImport) (RepositorySnapshot, error)
	Commit(context.Context, int64, RepositorySnapshot) (RepositorySnapshot, error)
}

var ErrRepositoryConflict = errors.New("device_repository_conflict")

// OpenWithRepository is the production state path. It never creates the
// legacy mobile directory: an empty new workspace remains write-free until a
// real settings/task change is committed by the host repository.
func OpenWithRepository(dir string, planner Planner, repository Repository) (*Control, error) {
	if repository == nil {
		return nil, errors.New("device_repository_required")
	}
	ctx := context.Background()
	snapshot, err := repository.Load(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.Revision < 0 || snapshot.Initialized != (snapshot.Revision > 0) {
		return nil, errors.New("device_repository_invalid")
	}
	if snapshot.Revision == 0 {
		legacy, found, err := readLegacyImport(dir)
		if err != nil {
			return nil, err
		}
		if found {
			snapshot, err = repository.Initialize(ctx, legacy)
			if err != nil {
				return nil, err
			}
			if snapshot.Revision == 0 || !snapshot.Initialized {
				return nil, errors.New("device_repository_invalid")
			}
		} else {
			snapshot = RepositorySnapshot{Settings: Defaults(), Tasks: map[string]Task{}}
		}
	}
	if err := validateRepositorySnapshot(snapshot); err != nil {
		return nil, err
	}
	c := newControl(dir, planner)
	c.repository, c.repoRevision = repository, snapshot.Revision
	c.settings, c.tasks = clone(snapshot.Settings), clone(snapshot.Tasks)
	if normalizeInterrupted(c.tasks) {
		if err := c.persistTasks(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func newControl(dir string, planner Planner) *Control {
	return &Control{dir: dir, settings: Defaults(), tasks: map[string]Task{}, running: map[string]context.CancelFunc{}, devices: map[string]string{}, planner: planner}
}

func validateRepositorySnapshot(snapshot RepositorySnapshot) error {
	if snapshot.Tasks == nil || len(snapshot.Tasks) > 200 || snapshot.Settings.Validate() != nil {
		return errors.New("device_repository_invalid")
	}
	requestHashes := make(map[string]string, len(snapshot.Tasks))
	for id, task := range snapshot.Tasks {
		if id != task.ID || !idPattern.MatchString(id) || task.RequestHash == "" {
			return errors.New("device_migration_required")
		}
		if other, exists := requestHashes[task.RequestHash]; exists && other != id {
			return errors.New("device_migration_required")
		}
		requestHashes[task.RequestHash] = id
	}
	return nil
}

func normalizeInterrupted(tasks map[string]Task) bool {
	changed := false
	for id, task := range tasks {
		if task.State == "running" || task.State == "queued" {
			task.State, task.Reason, task.Updated = "needs_review", "device_worker_interrupted_reobserve_required", time.Now().UTC()
			tasks[id], changed = task, true
		}
	}
	return changed
}

func readLegacyImport(dir string) (LegacyImport, bool, error) {
	root, err := openLegacyRoot(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LegacyImport{}, false, nil
		}
		return LegacyImport{}, false, errors.New("device_migration_required")
	}
	defer root.Close()
	settingsFile, err := readOptionalLegacy(root, "settings.json")
	if err != nil {
		return LegacyImport{}, false, err
	}
	tasksFile, err := readOptionalLegacy(root, "tasks.json")
	if err != nil {
		return LegacyImport{}, false, err
	}
	if !settingsFile.found && !tasksFile.found {
		return LegacyImport{}, false, nil
	}
	// Verify both paths again after all reads. This catches replacement or
	// concurrent writes before the host state becomes active; we never repair
	// or overwrite the source JSON on failure.
	if err := verifyOptionalLegacy(root, settingsFile); err != nil {
		return LegacyImport{}, false, err
	}
	if err := verifyOptionalLegacy(root, tasksFile); err != nil {
		return LegacyImport{}, false, err
	}
	settings := Defaults()
	if settingsFile.found && json.Unmarshal(settingsFile.data, &settings) != nil {
		return LegacyImport{}, false, errors.New("device_migration_required")
	}
	tasks := map[string]Task{}
	if tasksFile.found && json.Unmarshal(tasksFile.data, &tasks) != nil {
		return LegacyImport{}, false, errors.New("device_migration_required")
	}
	snapshot := RepositorySnapshot{Settings: settings, Tasks: tasks, Revision: 1, Initialized: true}
	if err := validateRepositorySnapshot(snapshot); err != nil {
		return LegacyImport{}, false, err
	}
	normalizeInterrupted(tasks)
	hash := sha256.New()
	_, _ = hash.Write([]byte("mobile-legacy-v1\x00"))
	for _, item := range []struct {
		found bool
		body  []byte
	}{{settingsFile.found, settingsFile.data}, {tasksFile.found, tasksFile.data}} {
		if item.found {
			_, _ = hash.Write([]byte{1})
			_, _ = hash.Write(item.body)
		} else {
			_, _ = hash.Write([]byte{0})
		}
		_, _ = hash.Write([]byte{0})
	}
	return LegacyImport{Settings: settings, Tasks: tasks, SourceHash: hex.EncodeToString(hash.Sum(nil)), ManifestVersion: 1}, true, nil
}

type legacyFile struct {
	name   string
	found  bool
	data   []byte
	digest string
	mode   os.FileMode
	size   int64
	mtime  time.Time
}

func openLegacyRoot(dir string) (*os.Root, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeIrregular != 0 {
		return nil, errors.New("legacy directory unsafe")
	}
	return os.OpenRoot(dir)
}

func readOptionalLegacy(root *os.Root, name string) (legacyFile, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return legacyFile{name: name}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeIrregular != 0 || info.Size() > 4<<20 {
		return legacyFile{}, errors.New("device_migration_required")
	}
	file, err := root.Open(name)
	if err != nil {
		return legacyFile{}, errors.New("device_migration_required")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4<<20+1))
	if err != nil || len(data) > 4<<20 {
		return legacyFile{}, errors.New("device_migration_required")
	}
	opened, err := file.Stat()
	if err != nil || !sameLegacyFile(info, opened) {
		return legacyFile{}, errors.New("device_migration_required")
	}
	digest := sha256.Sum256(data)
	return legacyFile{name: name, found: true, data: data, digest: hex.EncodeToString(digest[:]), mode: info.Mode(), size: info.Size(), mtime: info.ModTime()}, nil
}

func verifyOptionalLegacy(root *os.Root, want legacyFile) error {
	if !want.found {
		if _, err := root.Lstat(want.name); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("device_migration_required")
	}
	got, err := readOptionalLegacy(root, want.name)
	if err != nil || !got.found || got.digest != want.digest || got.mode != want.mode || got.size != want.size || !got.mtime.Equal(want.mtime) {
		return errors.New("device_migration_required")
	}
	return nil
}

func sameLegacyFile(before, after os.FileInfo) bool {
	return before.Mode() == after.Mode() && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}
