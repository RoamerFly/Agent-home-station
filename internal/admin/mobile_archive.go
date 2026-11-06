package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	mobile "wechat-bot/internal/devicespec"
	"wechat-bot/internal/pluginhost"
)

const mobileRetirementReason = "device_feature_retired"

// This adapter exposes the existing history readers without initializing a
// planner, scheduler, or device bridge.
type mobileArchiveService struct {
	history       *mobileWorkflowService
	retirementErr error
}

func (s *mobileArchiveService) Settings() mobile.Settings { return mobile.Defaults() }
func (s *mobileArchiveService) List(source *mobile.Source) []mobile.Task {
	return s.history.List(source)
}
func (s *mobileArchiveService) Get(id string) (mobile.Task, bool) { return s.history.Get(id) }
func (s *mobileArchiveService) Screenshot(taskID, observationID string) ([]byte, error) {
	return s.history.Screenshot(taskID, observationID)
}
func (s *mobileArchiveService) Artifact(taskID, artifactID string) (mobile.Artifact, []byte, error) {
	return s.history.Artifact(taskID, artifactID)
}
func (s *mobileArchiveService) Save(mobile.Settings) error { return errDeviceServiceRetired }
func (s *mobileArchiveService) Start(mobile.Source, string, string) (mobile.Task, error) {
	return mobile.Task{}, errDeviceServiceRetired
}
func (s *mobileArchiveService) Cancel(string, mobile.Source) error { return errDeviceServiceRetired }
func (s *mobileArchiveService) Devices(context.Context) (json.RawMessage, error) {
	return nil, errDeviceServiceRetired
}
func (s *mobileArchiveService) Busy() bool     { return false }
func (s *mobileArchiveService) StopAll() error { return nil }
func (s *mobileArchiveService) Close() error   { return nil }
func (s *mobileArchiveService) InitializationError() error {
	if s.retirementErr != nil {
		return s.retirementErr
	}
	return s.history.InitializationError()
}

func (s *mobileArchiveService) retire() error {
	deviceErr := s.retireDeviceJobs()
	agentErr := s.history.manager.agent.retireDeviceContinuations(s.history.manager.native.Account())
	s.retirementErr = errors.Join(deviceErr, agentErr)
	return s.retirementErr
}

func (s *mobileArchiveService) retireDeviceJobs() error {
	s.history.stateMu.Lock()
	defer s.history.stateMu.Unlock()
	snapshot, store, err := s.history.load(false)
	if err != nil || store == nil {
		return err
	}

	puts := map[string]json.RawMessage{}
	now := time.Now().UTC()
	for id, task := range snapshot.tasks {
		if mobileWorkflowActive(task) {
			task.State, task.Reason, task.Updated = "stopped", mobileRetirementReason, now
			puts[mobileTaskRecordKey(id)], _ = json.Marshal(task)
			// Keep the original claim and fact as evidence. The terminal task,
			// disabled settings, and retired runtime prevent their execution.
		}
	}
	if snapshot.settings.Enabled {
		snapshot.settings.Enabled = false
		puts["settings"], _ = json.Marshal(snapshot.settings)
	}
	if len(puts) > 0 {
		if err := s.history.commit(snapshot, store, puts); err != nil {
			return err
		}
	}
	return retireMobilePackageJobs(store)
}

func retireMobilePackageJobs(store *pluginhost.Store) error {
	ctx := context.Background()
	installed, err := store.Installed(ctx, mobileDevicePluginID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && !installed.Removed && installed.Enabled {
		if _, err := store.SetEnabled(ctx, mobileDevicePluginID, installed.Revision, false); err != nil {
			return err
		}
	}
	for next := int64(0); ; {
		page, err := store.Jobs(ctx, mobileDevicePluginID, next, 100)
		if err != nil {
			return err
		}
		for _, job := range page.Items {
			if job.Recovery() == "terminal" || job.CancelRequested {
				continue
			}
			// An already-dispatched operation has an unknown outcome. Preserve
			// its receipt and record the local stop request without replay.
			if _, err := store.RequestJobCancel(ctx, job.ID, job.Revision); err != nil {
				return err
			}
		}
		if !page.HasMore {
			return nil
		}
		next = page.Next
	}
}

func (a *agentControl) retireDeviceContinuations(account string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	old := map[string]agentTask{}
	now := time.Now().UTC()
	for id, task := range a.data.Tasks {
		if task.Account != account || task.DeviceTask == "" {
			continue
		}
		switch task.State {
		case "completed", "failed", "cancelled", "stopped":
			continue
		}
		old[id] = task
		task.State, task.Updated = "stopped", now
		if task.LastResult != "" {
			task.LastResult += "\n"
		}
		task.LastResult += "手机控制已停用，保留原任务及已有结果"
		a.data.Tasks[id] = task
	}
	if len(old) == 0 {
		return nil
	}
	if err := a.persist(); err != nil {
		for id, task := range old {
			a.data.Tasks[id] = task
		}
		return err
	}
	return a.retireSettledLocked()
}
