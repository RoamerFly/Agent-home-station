package admin

import (
	"context"
	"encoding/json"
	"errors"

	mobile "wechat-bot/internal/devicespec"
)

// deviceService is the stable host boundary for device control. It keeps the
// legacy Control implementation behind a lazy adapter so a chat-only install
// has neither a device directory nor a device runtime to initialize.
type deviceService interface {
	Settings() mobile.Settings
	Save(mobile.Settings) error
	List(*mobile.Source) []mobile.Task
	Get(string) (mobile.Task, bool)
	Start(mobile.Source, string, string) (mobile.Task, error)
	Cancel(string, mobile.Source) error
	Busy() bool
	StopAll() error
	Close() error
	Devices(context.Context) (json.RawMessage, error)
	Screenshot(string, string) ([]byte, error)
	Artifact(string, string) (mobile.Artifact, []byte, error)
	InitializationError() error
}

var (
	errDeviceServiceClosed      = errors.New("device_service_closed")
	errDeviceServiceUnavailable = errors.New("device_service_unavailable")
)

func (m *Manager) newMobileService() deviceService { return newMobileWorkflowService(m) }
