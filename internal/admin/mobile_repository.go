package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"wechat-bot/internal/mobile"
	"wechat-bot/internal/pluginhost"
)

const mobileStateNamespace = "device-state-v1"

// mobileRepository keeps device workflows separate from individual plugin
// invocation Jobs. Only the host can read this namespace; it is not a plugin
// configuration value or a directory mounted into the isolated runtime.
type mobileRepository struct {
	mu      sync.Mutex
	store   *pluginhost.Store
	current pluginhost.HostState
}

type mobileImportManifest struct {
	Version    int    `json:"version"`
	SourceHash string `json:"source_hash"`
	TaskCount  int    `json:"task_count"`
}

func (r *mobileRepository) Load(ctx context.Context) (mobile.RepositorySnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, err := r.store.ReadHostState(ctx, mobileDevicePluginID, mobileStateNamespace)
	if err != nil {
		return mobile.RepositorySnapshot{}, err
	}
	snapshot, err := decodeMobileHostState(state, r.store.Account())
	if err == nil {
		r.current = state
	}
	return snapshot, err
}

func decodeMobileHostState(state pluginhost.HostState, account string) (mobile.RepositorySnapshot, error) {
	out := mobile.RepositorySnapshot{Settings: mobile.Defaults(), Tasks: map[string]mobile.Task{}, Revision: state.Revision, Initialized: state.Revision > 0}
	if state.Revision == 0 {
		return out, nil
	}
	invalid := errors.New("device_repository_invalid")
	var manifest mobileImportManifest
	if state.Revision < 1 || json.Unmarshal(state.Records["manifest"], &manifest) != nil || manifest.Version != 1 || manifest.SourceHash != state.SourceHash || json.Unmarshal(state.Records["settings"], &out.Settings) != nil {
		return mobile.RepositorySnapshot{}, invalid
	}
	for key, record := range state.Records {
		if key == "settings" || key == "manifest" {
			continue
		}
		if !strings.HasPrefix(key, "task:") {
			return mobile.RepositorySnapshot{}, invalid
		}
		var task mobile.Task
		if json.Unmarshal(record, &task) != nil || key != mobileTaskRecordKey(task.ID) || task.ID == "" || task.RequestHash == "" || task.Source.Account != "" && task.Source.Account != account {
			return mobile.RepositorySnapshot{}, invalid
		}
		out.Tasks[task.ID] = task
	}
	return out, nil
}

func mobileTaskRecordKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "task:" + hex.EncodeToString(sum[:])
}

func mobileStateRecords(settings mobile.Settings, tasks map[string]mobile.Task, account string) (map[string]json.RawMessage, error) {
	records := make(map[string]json.RawMessage, len(tasks)+1)
	raw, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	records["settings"] = raw
	for id, task := range tasks {
		if id == "" || id != task.ID || task.RequestHash == "" || task.Source.Account != "" && task.Source.Account != account {
			return nil, errors.New("device_repository_invalid")
		}
		raw, err = json.Marshal(task)
		if err != nil {
			return nil, err
		}
		records[mobileTaskRecordKey(id)] = raw
	}
	return records, nil
}

func (r *mobileRepository) Initialize(ctx context.Context, legacy mobile.LegacyImport) (mobile.RepositorySnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if legacy.ManifestVersion != 1 {
		return mobile.RepositorySnapshot{}, errors.New("device_migration_required")
	}
	records, err := mobileStateRecords(legacy.Settings, legacy.Tasks, r.store.Account())
	if err != nil {
		return mobile.RepositorySnapshot{}, err
	}
	records["manifest"], _ = json.Marshal(mobileImportManifest{Version: 1, SourceHash: legacy.SourceHash, TaskCount: len(legacy.Tasks)})
	state, err := r.store.InitializeHostState(ctx, mobileDevicePluginID, mobileStateNamespace, legacy.SourceHash, records)
	if err != nil {
		return mobile.RepositorySnapshot{}, mobileRepositoryError(err)
	}
	r.current = state
	return decodeMobileHostState(state, r.store.Account())
}

func mobileRepositoryError(err error) error {
	if errors.Is(err, pluginhost.ErrRevision) {
		return mobile.ErrRepositoryConflict
	}
	return err
}

func (r *mobileRepository) Commit(ctx context.Context, revision int64, next mobile.RepositorySnapshot) (mobile.RepositorySnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if revision < 0 || r.current.Revision != revision {
		return mobile.RepositorySnapshot{}, mobile.ErrRepositoryConflict
	}
	records, err := mobileStateRecords(next.Settings, next.Tasks, r.store.Account())
	if err != nil {
		return mobile.RepositorySnapshot{}, err
	}
	if revision == 0 {
		// A fresh install has no JSON source. Its first explicit save creates
		// the authoritative namespace in one transaction, without legacy files.
		sum := sha256.Sum256([]byte("mobile-empty-origin-v1"))
		sourceHash := hex.EncodeToString(sum[:])
		records["manifest"], _ = json.Marshal(mobileImportManifest{Version: 1, SourceHash: sourceHash})
		state, err := r.store.InitializeHostState(ctx, mobileDevicePluginID, mobileStateNamespace, sourceHash, records)
		if err != nil {
			return mobile.RepositorySnapshot{}, mobileRepositoryError(err)
		}
		r.current = state
		return decodeMobileHostState(state, r.store.Account())
	}
	// The import manifest stays immutable. Each update touches only changed
	// settings/tasks, even though Control provides its bounded in-memory view.
	records["manifest"] = r.current.Records["manifest"]
	puts := map[string]json.RawMessage{}
	deletes := []string{}
	for key, raw := range records {
		if !bytes.Equal(raw, r.current.Records[key]) {
			puts[key] = raw
		}
	}
	for key := range r.current.Records {
		if _, exists := records[key]; !exists {
			deletes = append(deletes, key)
		}
	}
	updated, err := r.store.UpdateHostState(ctx, mobileDevicePluginID, mobileStateNamespace, revision, puts, deletes)
	if err != nil {
		return mobile.RepositorySnapshot{}, mobileRepositoryError(err)
	}
	r.current = pluginhost.HostState{Revision: updated, SourceHash: r.current.SourceHash, Records: records}
	return decodeMobileHostState(r.current, r.store.Account())
}

var _ mobile.Repository = (*mobileRepository)(nil)
