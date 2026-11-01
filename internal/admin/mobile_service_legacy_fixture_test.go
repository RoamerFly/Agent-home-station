package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"wechat-bot/internal/mobile"
)

type mobileControlOpener func(string, mobile.Planner) (*mobile.Control, error)

// lazyMobileService opens Control exactly once per successful lifetime. Failed
// opens are intentionally not cached: correcting an old settings/tasks file
// makes the next request retry it without any default overwrite.
type lazyMobileService struct {
	mu        sync.Mutex
	dir       string
	planner   mobile.Planner
	open      mobileControlOpener
	configure func(*mobile.Control) error
	// persistedState detects the active host repository after legacy JSON is
	// removed; nil keeps isolated legacy compatibility fixtures unchanged.
	persistedState func() (bool, error)
	control        *mobile.Control
	closed         bool
}

func newLazyMobileService(dir string, planner mobile.Planner, opener mobileControlOpener, configure func(*mobile.Control) error) *lazyMobileService {
	if opener == nil {
		opener = mobile.Open
	}
	return &lazyMobileService{dir: dir, planner: planner, open: opener, configure: configure}
}

// hasLegacyMobileState does not create the mobile directory. A directory with
// screenshots alone is not a configured service and must remain cold.
func (s *lazyMobileService) hasLegacyMobileState() (bool, error) {
	if s.persistedState != nil {
		if active, err := s.persistedState(); err != nil || active {
			return active, err
		}
	}
	info, err := os.Stat(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, errors.New("device_storage_invalid")
	}
	for _, name := range []string{"settings.json", "tasks.json"} {
		_, err = os.Stat(filepath.Join(s.dir, name))
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

// controlFor returns nil,nil for a cold read of an unconfigured installation.
// Opening only reads bounded local JSON and registers callbacks; it does not
// start a device worker. Holding the one service mutex across that work avoids
// a second state machine and makes Close wait for a fully detached control.
// create permits Save/Start to create the legacy directory deliberately.
func (s *lazyMobileService) controlFor(create bool) (*mobile.Control, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errDeviceServiceClosed
	}
	if s.control != nil {
		return s.control, nil
	}
	if !create {
		exists, err := s.hasLegacyMobileState()
		if err != nil || !exists {
			return nil, err
		}
	}
	control, err := s.open(s.dir, s.planner)
	if err != nil {
		return nil, err
	}
	if control == nil {
		return nil, errors.New("device_control_unavailable")
	}
	if s.configure != nil {
		if err := s.configure(control); err != nil {
			_ = control.Close()
			return nil, err
		}
	}
	s.control = control
	return control, nil
}

func (s *lazyMobileService) Settings() mobile.Settings {
	control, err := s.controlFor(false)
	if err != nil || control == nil {
		return mobile.Defaults()
	}
	return control.Settings()
}

func (s *lazyMobileService) Save(settings mobile.Settings) error {
	control, err := s.controlFor(true)
	if err != nil {
		return err
	}
	return control.Save(settings)
}

func (s *lazyMobileService) List(source *mobile.Source) []mobile.Task {
	control, err := s.controlFor(false)
	if err != nil || control == nil {
		return []mobile.Task{}
	}
	return control.List(source)
}

func (s *lazyMobileService) Get(id string) (mobile.Task, bool) {
	control, err := s.controlFor(false)
	if err != nil || control == nil {
		return mobile.Task{}, false
	}
	return control.Get(id)
}

func (s *lazyMobileService) Start(source mobile.Source, device, goal string) (mobile.Task, error) {
	control, err := s.controlFor(true)
	if err != nil {
		return mobile.Task{}, err
	}
	return control.Start(source, device, goal)
}

func (s *lazyMobileService) Cancel(id string, source mobile.Source) error {
	control, err := s.controlFor(false)
	if err != nil {
		return err
	}
	if control == nil {
		return errDeviceServiceUnavailable
	}
	return control.Cancel(id, source)
}

// Busy is intentionally non-opening. A service never opened in this process
// has no worker that can still be running.
func (s *lazyMobileService) Busy() bool {
	s.mu.Lock()
	control := s.control
	s.mu.Unlock()
	return control != nil && control.Busy()
}

func (s *lazyMobileService) StopAll() error {
	s.mu.Lock()
	control := s.control
	s.mu.Unlock()
	if control == nil {
		return nil
	}
	return control.StopAll()
}

func (s *lazyMobileService) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	control := s.control
	s.control = nil
	s.mu.Unlock()
	if control != nil {
		return control.Close()
	}
	return nil
}

func (s *lazyMobileService) Devices(ctx context.Context) (json.RawMessage, error) {
	control, err := s.controlFor(false)
	if err != nil {
		return nil, err
	}
	if control == nil {
		return nil, errors.New("device_control_disabled")
	}
	return control.Devices(ctx)
}

func (s *lazyMobileService) Screenshot(taskID, observationID string) ([]byte, error) {
	control, err := s.controlFor(false)
	if err != nil {
		return nil, err
	}
	if control == nil {
		return nil, errDeviceServiceUnavailable
	}
	return control.Screenshot(taskID, observationID)
}

func (s *lazyMobileService) Artifact(taskID, artifactID string) (mobile.Artifact, []byte, error) {
	control, err := s.controlFor(false)
	if err != nil {
		return mobile.Artifact{}, nil, err
	}
	if control == nil {
		return mobile.Artifact{}, nil, errDeviceServiceUnavailable
	}
	return control.Artifact(taskID, artifactID)
}

// InitializationError returns the original storage/validation error. A
// management route can expose it as a 503 while chat keeps running. Each call
// retries a repaired legacy directory instead of caching a stale failure.
func (s *lazyMobileService) InitializationError() error {
	_, err := s.controlFor(false)
	return err
}

var _ deviceService = (*lazyMobileService)(nil)
