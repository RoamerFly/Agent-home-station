// Package devicespec contains device wire/state types and pure validation.
// It starts no controller, goroutine, process or device operation. The legacy
// import helper only reads pinned source files.
package devicespec

import (
	"context"
	"encoding/json"
	"time"
)

type Source struct {
	Account   string `json:"account"`
	Thread    string `json:"thread"`
	Sender    string `json:"sender"`
	MessageID string `json:"message_id"`
	Admin     bool   `json:"admin"`
}
type Event struct {
	At      time.Time `json:"at"`
	State   string    `json:"state"`
	Action  string    `json:"action,omitempty"`
	Summary string    `json:"summary,omitempty"`
}
type Task struct {
	Artifacts   []Artifact `json:"artifacts,omitempty"`
	RequestHash string     `json:"request_hash"`
	ID          string     `json:"id"`
	DeviceID    string     `json:"device_id"`
	// FishID and DeviceBindingID bind a task to the fish identity and to one
	// explicit, revocable phone association.  They are deliberately optional
	// so pre-identity owner tasks retain their historical control semantics.
	FishID          string    `json:"fish_id,omitempty"`
	DeviceBindingID string    `json:"device_binding_id,omitempty"`
	Goal            string    `json:"goal"`
	State           string    `json:"state"`
	Source          Source    `json:"source"`
	Steps           int       `json:"steps"`
	ObservationID   string    `json:"observation_id,omitempty"`
	Result          string    `json:"result,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	Created         time.Time `json:"created"`
	Updated         time.Time `json:"updated"`
	Events          []Event   `json:"events"`
}
type Artifact struct {
	ID               string `json:"id"`
	DeviceID         string `json:"device_id"`
	RequestKey       string `json:"request_key"`
	Kind             string `json:"kind"`
	MIME             string `json:"mime"`
	Bytes            int    `json:"bytes"`
	SHA256           string `json:"sha256"`
	RequestedSeconds int    `json:"requested_seconds"`
	Audio            bool   `json:"audio"`
	Original         bool   `json:"original_media"`
	Summary          string `json:"summary,omitempty"`
	AssetID          string `json:"asset_id,omitempty"`
}
type ArtifactAnalyzer func(context.Context, Task, Artifact, []byte) (summary, assetID string, err error)
type Observation struct {
	AllowedPackages []string `json:"allowed_packages,omitempty"`
	Browsers        []string `json:"browsers,omitempty"`
	WebHosts        []string `json:"web_hosts,omitempty"`
	ID              string   `json:"id"`
	DeviceID        string   `json:"device_id"`
	Package         string   `json:"package"`
	// CapturedAt is set by the host immediately after it has validated and
	// durably stored the screenshot. It is deliberately not Task.Updated:
	// workflow bookkeeping and restarts must never rewrite the observed time.
	CapturedAt time.Time         `json:"captured_at,omitzero"`
	Width      int               `json:"width"`
	Height     int               `json:"height"`
	Nodes      []json.RawMessage `json:"nodes"`
	Raw        json.RawMessage   `json:"-"`
	PNG        []byte            `json:"-"`
}
type Step struct {
	Action    string         `json:"action"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Summary   string         `json:"summary,omitempty"`
}
type Planner func(context.Context, Task, Observation) (Step, error)
type Bridge interface {
	Call(context.Context, string, json.RawMessage) (string, error)
	Close() error
}

// BrokerFactory describes a device broker for one already-authorized
// operation.  The host supplies it when the device backend is implemented by
// an installed plugin.  Task is deliberately empty for the local management
// Devices operation; its Source is Admin so a binder can distinguish that
// narrow, unscoped listing operation from a conversation task.
//
// It is required for execution. A nil factory means the device plugin is not
// installed or not available; legacy Bridge settings are never executed by
// its caller. Errors (and a nil Bridge) are never retried through old command
// configuration, which would silently widen a plugin boundary.
type BrokerFactory func(context.Context, Task, Settings) (Bridge, error)
