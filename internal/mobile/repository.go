package mobile

import (
	"context"
	"errors"
	"time"

	"wechat-bot/internal/devicespec"
)

type RepositorySnapshot = devicespec.RepositorySnapshot
type LegacyImport = devicespec.LegacyImport
type Repository = devicespec.Repository

var ErrRepositoryConflict = devicespec.ErrRepositoryConflict

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
	return devicespec.ValidateRepositorySnapshot(snapshot)
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

// ReadLegacyImport exposes the pinned, unmodified legacy snapshot to the host.
// Recovery decisions belong to the installed workflow; source files are untouched.
func ReadLegacyImport(dir string) (LegacyImport, bool, error) {
	return devicespec.ReadLegacyImport(dir)
}
func readLegacyImport(dir string) (LegacyImport, bool, error) {
	value, found, err := devicespec.ReadLegacyImport(dir)
	if err == nil && found {
		normalizeInterrupted(value.Tasks)
	}
	return value, found, err
}
