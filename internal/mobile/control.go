// Package mobile owns durable device tasks independently of WeChat's receive
// loop. Screenshots and actions are supplied by a private owner-configured MCP.
package mobile

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
	"unicode/utf8"
	"wechat-bot/internal/mcptools"
)

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var observationPattern = regexp.MustCompile(`^obs-[a-f0-9]{32}$`)
var artifactPattern = regexp.MustCompile(`^clip-[a-f0-9]{32}$`)
var bridgeReasonPattern = regexp.MustCompile(`^device_[a-z_]{1,90}$`)
var errPermissionChanged = errors.New("device_permission_changed")

type Settings struct {
	Revision   int64                 `json:"revision"`
	Enabled    bool                  `json:"enabled"`
	Bridge     mcptools.ServerConfig `json:"bridge"`
	Devices    []string              `json:"devices"`
	Actions    []string              `json:"actions"`
	MaxSteps   int                   `json:"max_steps"`
	MaxSeconds int                   `json:"max_seconds"`
}

func Defaults() Settings {
	return Settings{Devices: []string{}, Actions: []string{}, MaxSteps: 12, MaxSeconds: 300, Bridge: mcptools.ServerConfig{ID: "device", Transport: "stdio", Enabled: true, TimeoutSeconds: 45, Tools: []string{"devices", "observe", "act"}}}
}
func (c Settings) Validate() error {
	if len(c.Bridge.Env) > 0 || len(c.Bridge.Headers) > 0 || c.Bridge.URL != "" {
		return errors.New("device_bridge_credentials_not_supported")
	}
	if c.MaxSteps < 1 || c.MaxSteps > 30 || c.MaxSeconds < 30 || c.MaxSeconds > 900 || len(c.Devices) > 16 || len(c.Actions) > 9 {
		return errors.New("device_configuration_invalid")
	}
	for _, id := range c.Devices {
		if !idPattern.MatchString(id) {
			return errors.New("device_id_invalid")
		}
	}
	for _, action := range c.Actions {
		if !includes([]string{"launch", "tap", "swipe", "text", "back", "home", "capture", "open_url"}, action) {
			return errors.New("device_action_invalid")
		}
	}
	if c.Enabled {
		if !c.Bridge.Enabled || c.Bridge.Transport != "stdio" || c.Bridge.ID != "device" {
			return errors.New("device_bridge_invalid")
		}
		h, e := mcptools.New([]mcptools.ServerConfig{c.Bridge})
		if e != nil {
			return errors.New("device_bridge_invalid")
		}
		_ = h.Close()
	}
	return nil
}
func includes(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

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
	Artifacts     []Artifact `json:"artifacts,omitempty"`
	RequestHash   string     `json:"request_hash"`
	ID            string     `json:"id"`
	DeviceID      string     `json:"device_id"`
	Goal          string     `json:"goal"`
	State         string     `json:"state"`
	Source        Source     `json:"source"`
	Steps         int        `json:"steps"`
	ObservationID string     `json:"observation_id,omitempty"`
	Result        string     `json:"result,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	Created       time.Time  `json:"created"`
	Updated       time.Time  `json:"updated"`
	Events        []Event    `json:"events"`
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
	AllowedPackages []string          `json:"allowed_packages,omitempty"`
	Browsers        []string          `json:"browsers,omitempty"`
	WebHosts        []string          `json:"web_hosts,omitempty"`
	ID              string            `json:"id"`
	DeviceID        string            `json:"device_id"`
	Package         string            `json:"package"`
	Width           int               `json:"width"`
	Height          int               `json:"height"`
	Nodes           []json.RawMessage `json:"nodes"`
	Raw             json.RawMessage   `json:"-"`
	PNG             []byte            `json:"-"`
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
type bridgeFactory func(Settings) (Bridge, error)

// BrokerFactory opens the production device broker for one already-authorized
// operation.  The host supplies it when the device backend is implemented by
// an installed plugin.  Task is deliberately empty for the local management
// Devices operation; its Source is Admin so a binder can distinguish that
// narrow, unscoped listing operation from a conversation task.
//
// A nil factory preserves the legacy in-process MCP bridge for compatibility
// with existing installations and isolated tests.  A non-nil factory is
// authoritative: errors (and a nil Bridge) are never retried through legacy
// configuration, which would otherwise silently widen a plugin boundary.
type BrokerFactory func(context.Context, Task, Settings) (Bridge, error)

type Control struct {
	mu            sync.Mutex
	dir           string
	settings      Settings
	tasks         map[string]Task
	running       map[string]context.CancelFunc
	devices       map[string]string
	planner       Planner
	factory       bridgeFactory
	brokerFactory BrokerFactory
	closed        bool
	wg            sync.WaitGroup
	finished      func(Task)
	authorize     func(Source) bool
	analyze       ArtifactAnalyzer
}
type hubBridge struct{ *mcptools.Hub }

func (h hubBridge) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	return h.Hub.Call(ctx, "device__"+name, args)
}
func Open(dir string, planner Planner) (*Control, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	c := &Control{dir: dir, settings: Defaults(), tasks: map[string]Task{}, running: map[string]context.CancelFunc{}, devices: map[string]string{}, planner: planner}
	c.factory = func(s Settings) (Bridge, error) {
		cfg := s.Bridge
		cfg.Tools = []string{"devices", "observe", "act", "read_artifact"}
		h, e := mcptools.New([]mcptools.ServerConfig{cfg})
		return hubBridge{h}, e
	}
	if err := load(filepath.Join(dir, "settings.json"), &c.settings); err != nil {
		return nil, err
	}
	if err := c.settings.Validate(); err != nil {
		return nil, err
	}
	if err := load(filepath.Join(dir, "tasks.json"), &c.tasks); err != nil {
		return nil, err
	}
	if c.tasks == nil || len(c.tasks) > 200 {
		return nil, errors.New("device_task_storage_invalid")
	}
	for id, t := range c.tasks {
		if id != t.ID || !idPattern.MatchString(id) {
			return nil, errors.New("device_task_storage_invalid")
		}
		if t.State == "running" || t.State == "queued" {
			t.State = "needs_review"
			t.Reason = "device_worker_interrupted_reobserve_required"
			t.Updated = time.Now().UTC()
			c.tasks[id] = t
		}
	}
	return c, c.persistTasks()
}
func load(path string, value any) error {
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil || len(b) > 4<<20 || json.Unmarshal(b, value) != nil {
		return errors.New("device_storage_invalid")
	}
	return nil
}
func write(path string, value any) error {
	b, e := json.Marshal(value)
	if e != nil || len(b) > 4<<20 {
		return errors.New("device_storage_limit")
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".device-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	_ = f.Chmod(0600)
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func (c *Control) persistTasks() error { return write(filepath.Join(c.dir, "tasks.json"), c.tasks) }
func clone[T any](value T) T {
	b, _ := json.Marshal(value)
	var copy T
	_ = json.Unmarshal(b, &copy)
	return copy
}
func (c *Control) Settings() Settings       { c.mu.Lock(); defer c.mu.Unlock(); return clone(c.settings) }
func (c *Control) OnFinished(fn func(Task)) { c.mu.Lock(); defer c.mu.Unlock(); c.finished = fn }

// SetBrokerFactory installs (or clears) the authoritative production bridge
// opener.  A running task has already captured its security context, so do not
// swap this boundary mid-run.  Queued tasks open the current factory only when
// they are actually scheduled.
func (c *Control) SetBrokerFactory(factory BrokerFactory) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("device_control_closed")
	}
	if len(c.running) != 0 {
		return errors.New("device_task_running")
	}
	c.brokerFactory = factory
	return nil
}

// SetAuthorizer is called before the control is exposed to requests.
func (c *Control) SetAuthorizer(fn func(Source) bool)      { c.authorize = fn }
func (c *Control) SetArtifactAnalyzer(fn ArtifactAnalyzer) { c.analyze = fn }
func (c *Control) Save(s Settings) error {
	if e := s.Validate(); e != nil {
		return e
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("device_control_closed")
	}
	if s.Revision != c.settings.Revision {
		return errors.New("device_configuration_changed")
	}
	s.Revision++
	if e := write(filepath.Join(c.dir, "settings.json"), s); e != nil {
		return e
	}
	c.settings = clone(s)
	for id, t := range c.tasks {
		if t.State == "queued" || t.State == "running" {
			t.State, t.Reason, t.Updated = "cancelled", "device_permission_changed", time.Now().UTC()
			c.tasks[id] = t
		}
	}
	persistErr := c.persistTasks()
	// Settings are already durable. Even if cancellation persistence fails,
	// keep the in-memory queue stopped; a restart converts old pending work
	// to needs_review rather than executing it under changed permissions.
	for _, cancel := range c.running {
		cancel()
	}
	return persistErr
}
func (c *Control) allowed(s Settings, source Source) bool {
	// The admin adapter verifies the existing trusted identity/thread policy.
	// Do not create another competing identity allowlist here.
	return s.Enabled && source.Admin && (c.authorize == nil || c.authorize(source))
}
func (c *Control) Allowed(source Source) bool { return c.allowed(c.Settings(), source) }
func (c *Control) List(source *Source) []Task {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []Task{}
	for _, t := range c.tasks {
		if source != nil && !source.Admin && (source.Account != t.Source.Account || source.Thread != t.Source.Thread) {
			continue
		}
		out = append(out, clone(t))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}
func (c *Control) Get(id string) (Task, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tasks[id]
	return clone(t), ok
}
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "device-" + hex.EncodeToString(b[:])
}
func (c *Control) Start(source Source, device, goal string) (Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.settings
	if c.closed || !c.allowed(s, source) || !includes(s.Devices, device) || c.planner == nil {
		return Task{}, errors.New("device_task_not_permitted")
	}
	if len(goal) == 0 || len(goal) > 3000 || !utf8.ValidString(goal) || source.MessageID == "" || len(source.MessageID) > 8192 {
		return Task{}, errors.New("device_task_input_limit")
	}
	b, _ := json.Marshal([]any{source, device, goal})
	hash := sha256.Sum256(b)
	requestHash := hex.EncodeToString(hash[:])
	for _, old := range c.tasks {
		if old.RequestHash == requestHash {
			return clone(old), nil
		}
	}
	if len(c.tasks) >= 200 {
		return Task{}, errors.New("device_task_storage_limit")
	}
	pending := 0
	for _, old := range c.tasks {
		if old.DeviceID == device && (old.State == "queued" || old.State == "running") {
			pending++
		}
	}
	if pending >= 8 {
		return Task{}, errors.New("device_queue_limit")
	}
	t := Task{ID: newID(), RequestHash: requestHash, DeviceID: device, Goal: goal, State: "queued", Source: source, Created: time.Now().UTC(), Updated: time.Now().UTC(), Events: []Event{}}
	c.tasks[t.ID] = t
	if e := c.persistTasks(); e != nil {
		delete(c.tasks, t.ID)
		return Task{}, e
	}
	c.scheduleLocked(device)
	return clone(t), nil
}
func (c *Control) scheduleLocked(device string) {
	if c.closed || !c.settings.Enabled || c.devices[device] != "" {
		return
	}
	var next *Task
	for _, t := range c.tasks {
		if t.DeviceID != device || t.State != "queued" {
			continue
		}
		if next == nil || t.Created.Before(next.Created) || t.Created.Equal(next.Created) && t.ID < next.ID {
			copy := t
			next = &copy
		}
	}
	if next == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.settings.MaxSeconds)*time.Second)
	c.running[next.ID], c.devices[device] = cancel, next.ID
	c.wg.Add(1)
	go c.run(ctx, cancel, next.ID, clone(c.settings))
}
func (c *Control) Cancel(id string, source Source) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tasks[id]
	if !ok || !c.cancelAllowed(source, t.Source) {
		return errors.New("device_task_not_found")
	}
	if t.State == "completed" || t.State == "cancelled" {
		return nil
	}
	old := clone(t)
	t.State, t.Reason, t.Updated = "cancelled", "device_cancelled_by_owner", time.Now().UTC()
	c.tasks[id] = t
	if e := c.persistTasks(); e != nil {
		c.tasks[id] = old
		return e
	}
	if cancel := c.running[id]; cancel != nil {
		cancel()
	}
	return nil
}

// cancelAllowed deliberately differs from Start: a chat-origin request is
// allowed to stop only its exact original task, even when the adapter marks it
// non-admin. A management-page request has no conversation scope and must set
// Admin; an in-conversation Admin bit is never a global cancellation grant.
// Keep the dynamic adapter authorizer in both cases so revocation wins.
func (c *Control) cancelAllowed(source, original Source) bool {
	// Empty identity is reserved for the local management surface; it must not
	// accidentally match a task that was also created without chat scope.
	sameSource := source.Account != "" && source.Thread != "" && source.Sender != "" && source.Account == original.Account && source.Thread == original.Thread && source.Sender == original.Sender
	managementSurface := source.Admin && source.Account == "" && source.Thread == "" && source.Sender == ""
	if !sameSource && !managementSurface {
		return false
	}
	// Chat adapters intentionally mark a normal cancel command as non-admin,
	// while Start was authorized using the persisted request source. Once the
	// caller has proved it is the original requester (or the local unscoped
	// management surface), revalidate that original authority. Never promote an
	// arbitrary same-room Admin bit into a global cancellation privilege.
	if c.authorize == nil {
		return true
	}
	if managementSurface {
		// A separately authenticated local management surface must retain its
		// ability to stop an orphaned task after the original chat requester was
		// revoked or the Agent was paused.
		return c.authorize(source)
	}
	return c.authorize(original)
}
func (c *Control) update(id, state, action, summary, reason, observation string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.tasks[id]
	t := clone(old)
	if t.State == "cancelled" {
		return context.Canceled
	}
	t.State, t.Reason, t.Updated = state, reason, time.Now().UTC()
	if observation != "" {
		t.ObservationID = observation
	}
	if len(summary) > 2400 {
		summary = summary[:2400]
		for !utf8.ValidString(summary) {
			summary = summary[:len(summary)-1]
		}
	}
	if state == "completed" || state == "needs_review" && summary != "" {
		t.Result = summary
	}
	t.Events = append(t.Events, Event{At: t.Updated, State: state, Action: action, Summary: summary})
	if len(t.Events) > 60 {
		t.Events = t.Events[len(t.Events)-60:]
	}
	if action != "" {
		t.Steps++
	}
	c.tasks[id] = t
	if e := c.persistTasks(); e != nil {
		c.tasks[id] = old
		return e
	}
	if observation != "" && old.ObservationID != observation && observationPattern.MatchString(old.ObservationID) {
		_ = os.Remove(filepath.Join(c.dir, "observations", old.ObservationID+".png"))
	}
	return nil
}
func (c *Control) observe(ctx context.Context, b Bridge, device string) (Observation, error) {
	var out Observation
	args, _ := json.Marshal(map[string]string{"device_id": device})
	raw, e := b.Call(ctx, "observe", args)
	if e != nil {
		if bridgeReasonPattern.MatchString(raw) {
			return out, errors.New(raw)
		}
		return out, e
	}
	var result struct {
		Text  string `json:"text"`
		Media []struct {
			MIME string `json:"mime_type"`
			Data string `json:"data_base64"`
		} `json:"media"`
	}
	if json.Unmarshal([]byte(raw), &result) != nil || json.Unmarshal([]byte(result.Text), &out) != nil || len(result.Media) != 1 || result.Media[0].MIME != "image/png" || out.DeviceID != device || !observationPattern.MatchString(out.ID) || out.Width < 1 || out.Height < 1 || out.Width > 4096 || out.Height > 4096 || len(out.Nodes) > 100 {
		return out, errors.New("device_observation_invalid")
	}
	out.PNG, e = base64.StdEncoding.DecodeString(result.Media[0].Data)
	if e != nil || len(out.PNG) > 650<<10 || len(out.PNG) < 24 {
		return Observation{}, errors.New("device_screenshot_invalid")
	}
	image, e := png.DecodeConfig(bytes.NewReader(out.PNG))
	if e != nil || image.Width != out.Width || image.Height != out.Height {
		return Observation{}, errors.New("device_screenshot_invalid")
	}
	out.Raw = json.RawMessage(result.Text)
	dir := filepath.Join(c.dir, "observations")
	if e = os.MkdirAll(dir, 0700); e != nil {
		return out, e
	}
	if e = os.WriteFile(filepath.Join(dir, out.ID+".png"), out.PNG, 0600); e != nil {
		return out, e
	}
	return out, nil
}

// currentRunnable reads the durable task state before a sensitive hand-off.
// It deliberately does not require State == running: the first observation is
// taken while a newly scheduled task is still queued. The caller must still
// check its context because cancellation can race immediately after this read.
func (c *Control) currentRunnable(id string) (Task, bool) {
	c.mu.Lock()
	t, ok := c.tasks[id]
	s := clone(c.settings)
	closed := c.closed
	c.mu.Unlock()
	if !ok || closed || t.State == "cancelled" {
		return Task{}, false
	}
	t = clone(t)
	return t, c.allowed(s, t.Source)
}

// taskBridgeInput takes a fresh durable snapshot immediately before opening a
// side-effecting backend.  In particular it does not trust the Settings copy
// captured when a task was queued: Save cancels running work, but a revoked
// authorization or expired context must also stop a just-to-start bridge.
func (c *Control) taskBridgeInput(ctx context.Context, id string) (Task, Settings, BrokerFactory, error) {
	if err := ctx.Err(); err != nil {
		return Task{}, Settings{}, nil, err
	}
	c.mu.Lock()
	t, ok := c.tasks[id]
	s := clone(c.settings)
	factory := c.brokerFactory
	closed := c.closed
	c.mu.Unlock()
	if !ok || closed || t.State == "cancelled" || !c.allowed(s, t.Source) || ctx.Err() != nil {
		return Task{}, Settings{}, nil, errPermissionChanged
	}
	return clone(t), s, factory, nil
}

func (c *Control) openTaskBridge(ctx context.Context, id string) (Bridge, error) {
	t, s, factory, err := c.taskBridgeInput(ctx, id)
	if err != nil {
		return nil, err
	}
	if factory != nil {
		// This is intentionally not followed by c.factory.  A configured plugin
		// broker is a security boundary, not a best-effort preference.
		bridge, openErr := factory(ctx, t, s)
		if openErr != nil {
			return nil, openErr
		}
		if bridge == nil {
			return nil, errors.New("device_bridge_unavailable")
		}
		return bridge, nil
	}
	return c.factory(s)
}

func (c *Control) openManagementBridge(ctx context.Context) (Bridge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	management := Task{Source: Source{Admin: true}}
	c.mu.Lock()
	s := clone(c.settings)
	factory := c.brokerFactory
	closed := c.closed
	c.mu.Unlock()
	if closed || !c.allowed(s, management.Source) || ctx.Err() != nil {
		return nil, errPermissionChanged
	}
	if factory != nil {
		bridge, err := factory(ctx, management, s)
		if err != nil {
			return nil, err
		}
		if bridge == nil {
			return nil, errors.New("device_bridge_unavailable")
		}
		return bridge, nil
	}
	return c.factory(s)
}

func (c *Control) run(ctx context.Context, cancel context.CancelFunc, id string, s Settings) {
	defer c.wg.Done()
	defer cancel()
	defer func() {
		c.mu.Lock()
		delete(c.running, id)
		for device, owner := range c.devices {
			if owner == id {
				delete(c.devices, device)
			}
		}
		fn := c.finished
		t := clone(c.tasks[id])
		c.scheduleLocked(t.DeviceID)
		c.mu.Unlock()
		if fn != nil {
			fn(t)
		}
	}()
	ctx = mcptools.WithSessionScope(ctx, id)
	b, e := c.openTaskBridge(ctx, id)
	if e != nil {
		if errors.Is(e, errPermissionChanged) {
			_ = c.update(id, "stopped", "", "", "device_permission_changed", "")
			return
		}
		_ = c.update(id, "failed", "", "", "device_bridge_unavailable", "")
		return
	}
	defer b.Close()
	for n := 0; n < s.MaxSteps; n++ {
		t, ok := c.Get(id)
		if !ok || ctx.Err() != nil || t.State == "cancelled" {
			break
		}
		if !c.Allowed(t.Source) {
			_ = c.update(id, "stopped", "", "", "device_permission_changed", "")
			return
		}
		o, e := c.observe(ctx, b, t.DeviceID)
		if e != nil {
			reason := "device_observation_unavailable"
			if bridgeReasonPattern.MatchString(e.Error()) {
				reason = e.Error()
			}
			_ = c.update(id, "needs_review", "", "", reason, "")
			return
		}
		// The bridge invocation may itself take long enough for access to be
		// revoked. Never give that fresh screenshot to the planner afterward.
		if current, runnable := c.currentRunnable(id); ctx.Err() != nil || !runnable {
			if ctx.Err() == nil && current.ID != "" {
				_ = c.update(id, "stopped", "", "", "device_permission_changed", o.ID)
			}
			return
		}
		if e = c.update(id, "running", "", "", "", o.ID); e != nil {
			return
		}
		t, _ = c.Get(id)
		step, e := c.planner(ctx, t, o)
		if e != nil {
			_ = c.update(id, "failed", "", "", "device_model_unavailable", o.ID)
			return
		}
		if ctx.Err() != nil {
			break
		}
		// A planner may take arbitrarily long. Its result is never authority to
		// finish, block, wait, or act after the requesting source was revoked.
		if !c.Allowed(t.Source) {
			_ = c.update(id, "stopped", "", "", "device_permission_changed", o.ID)
			return
		}
		if step.Action == "blocked" {
			_ = c.update(id, "needs_review", "", step.Summary, "device_user_action_required", o.ID)
			return
		}
		if step.Action == "finish" {
			_ = c.update(id, "completed", "", step.Summary, "", o.ID)
			return
		}
		if step.Action == "wait" {
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
			continue
		}
		current := c.Settings()
		if !c.allowed(current, t.Source) || !includes(current.Actions, step.Action) {
			_ = c.update(id, "stopped", "", "", "device_action_not_permitted", o.ID)
			return
		}
		// Model calls can outlive the bridge's short snapshot TTL. Obtain a new
		// observation and only reuse coordinates when the app, dimensions and
		// pixels or exact UI nodes still match what the model actually saw.
		fresh, e := c.observe(ctx, b, t.DeviceID)
		if e != nil {
			reason := "device_observation_unavailable"
			if bridgeReasonPattern.MatchString(e.Error()) {
				reason = e.Error()
			}
			_ = c.update(id, "needs_review", "", "", reason, "")
			return
		}
		// Recheck after the second observation as well: a revoked requester must
		// not turn a previously approved plan into a device action.
		if current, runnable := c.currentRunnable(id); ctx.Err() != nil || !runnable {
			if ctx.Err() == nil && current.ID != "" {
				_ = c.update(id, "stopped", "", "", "device_permission_changed", fresh.ID)
			}
			return
		}
		same := fresh.Package == o.Package && fresh.Width == o.Width && fresh.Height == o.Height
		oldNodes, _ := json.Marshal(o.Nodes)
		newNodes, _ := json.Marshal(fresh.Nodes)
		if !same || !(step.Action == "capture" || bytes.Equal(o.PNG, fresh.PNG) || len(o.Nodes) > 0 && bytes.Equal(oldNodes, newNodes)) {
			if c.update(id, "running", "", "页面已变化，重新交给模型观察", "", fresh.ID) != nil {
				return
			}
			continue
		}
		o = fresh
		if step.Arguments == nil {
			step.Arguments = map[string]any{}
		}
		step.Arguments["device_id"], step.Arguments["observation_id"], step.Arguments["request_key"], step.Arguments["action"] = t.DeviceID, o.ID, id+"-step-"+hex.EncodeToString([]byte{byte(n)}), step.Action
		if e = c.update(id, "running", step.Action, "动作提交前检查点", "", o.ID); e != nil {
			return
		}
		// update() persists the checkpoint but must not be treated as an
		// authorization lease. Check the latest durable task and policy directly
		// before invoking the side-effecting bridge action.
		if current, runnable := c.currentRunnable(id); ctx.Err() != nil || !runnable {
			if ctx.Err() == nil && current.ID != "" {
				_ = c.update(id, "stopped", "", "", "device_permission_changed", o.ID)
			}
			return
		}
		args, _ := json.Marshal(step.Arguments)
		result, e := b.Call(ctx, "act", args)
		if e != nil {
			reason := "device_action_outcome_unknown"
			if bridgeReasonPattern.MatchString(result) {
				reason = result
			}
			_ = c.update(id, "needs_review", "", "", reason, o.ID)
			return
		}
		var status struct {
			State  string `json:"state"`
			Result struct {
				Artifact *Artifact `json:"artifact"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(result), &status) != nil || status.State != "submitted" {
			_ = c.update(id, "needs_review", "", "", "device_action_outcome_unknown", o.ID)
			return
		}
		if step.Action == "capture" {
			if !c.Allowed(t.Source) {
				_ = c.update(id, "stopped", "", "", "device_permission_changed", o.ID)
				return
			}
			artifactErr := error(nil)
			if status.Result.Artifact != nil {
				artifactErr = c.collectArtifact(ctx, b, t, *status.Result.Artifact, step.Arguments["request_key"].(string))
			}
			if status.Result.Artifact == nil || artifactErr != nil {
				if errors.Is(artifactErr, errPermissionChanged) {
					_ = c.update(id, "stopped", "", "", "device_permission_changed", o.ID)
					return
				}
				_ = c.update(id, "needs_review", "", "", "device_recording_unavailable", o.ID)
				return
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	t, ok := c.Get(id)
	if ok && t.State != "cancelled" {
		_ = c.update(id, "needs_review", "", "", "device_step_or_time_budget", "")
	}
}
func (c *Control) Close() error {
	c.mu.Lock()
	c.closed = true
	for _, cancel := range c.running {
		cancel()
	}
	c.mu.Unlock()
	c.wg.Wait()
	return nil
}
func (c *Control) Busy() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.running) > 0 }

// StopAll checkpoints cancellation before stopping workers. It does not undo
// an ADB action already submitted. New tasks remain subject to Settings.Enabled.
func (c *Control) StopAll() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, t := range c.tasks {
		if t.State == "queued" || t.State == "running" {
			t.State, t.Reason, t.Updated = "cancelled", "device_cancelled_by_owner", time.Now().UTC()
			c.tasks[id] = t
		}
	}
	err := c.persistTasks()
	for _, cancel := range c.running {
		cancel()
	}
	return err
}

func (c *Control) Devices(ctx context.Context) (json.RawMessage, error) {
	s := c.Settings()
	if !s.Enabled {
		return nil, errors.New("device_control_disabled")
	}
	b, err := c.openManagementBridge(ctx)
	if err != nil {
		return nil, err
	}
	defer b.Close()
	value, err := b.Call(ctx, "devices", json.RawMessage(`{}`))
	if err != nil || !json.Valid([]byte(value)) || len(value) > 16384 {
		return nil, errors.New("device_bridge_unavailable")
	}
	return json.RawMessage(value), nil
}
func (c *Control) Screenshot(taskID, observationID string) ([]byte, error) {
	t, ok := c.Get(taskID)
	if !ok || t.ObservationID != observationID || !observationPattern.MatchString(observationID) {
		return nil, errors.New("device_observation_not_found")
	}
	return os.ReadFile(filepath.Join(c.dir, "observations", observationID+".png"))
}
