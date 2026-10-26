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
	if c.MaxSteps < 1 || c.MaxSteps > 30 || c.MaxSeconds < 30 || c.MaxSeconds > 900 || len(c.Devices) > 16 || len(c.Actions) > 8 {
		return errors.New("device_configuration_invalid")
	}
	for _, id := range c.Devices {
		if !idPattern.MatchString(id) {
			return errors.New("device_id_invalid")
		}
	}
	for _, action := range c.Actions {
		if !includes([]string{"launch", "tap", "swipe", "text", "back", "home"}, action) {
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
	RequestHash   string    `json:"request_hash"`
	ID            string    `json:"id"`
	DeviceID      string    `json:"device_id"`
	Goal          string    `json:"goal"`
	State         string    `json:"state"`
	Source        Source    `json:"source"`
	Steps         int       `json:"steps"`
	ObservationID string    `json:"observation_id,omitempty"`
	Result        string    `json:"result,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	Created       time.Time `json:"created"`
	Updated       time.Time `json:"updated"`
	Events        []Event   `json:"events"`
}
type Observation struct {
	ID       string            `json:"id"`
	DeviceID string            `json:"device_id"`
	Package  string            `json:"package"`
	Width    int               `json:"width"`
	Height   int               `json:"height"`
	Nodes    []json.RawMessage `json:"nodes"`
	Raw      json.RawMessage   `json:"-"`
	PNG      []byte            `json:"-"`
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
type Control struct {
	mu        sync.Mutex
	dir       string
	settings  Settings
	tasks     map[string]Task
	running   map[string]context.CancelFunc
	devices   map[string]string
	planner   Planner
	factory   bridgeFactory
	closed    bool
	wg        sync.WaitGroup
	finished  func(Task)
	authorize func(Source) bool
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
		cfg.Tools = []string{"devices", "observe", "act"}
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

// SetAuthorizer is called before the control is exposed to requests.
func (c *Control) SetAuthorizer(fn func(Source) bool) { c.authorize = fn }
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
	for _, cancel := range c.running {
		cancel()
	}
	return nil
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
	if c.devices[device] != "" {
		return Task{}, errors.New("device_busy")
	}
	t := Task{ID: newID(), RequestHash: requestHash, DeviceID: device, Goal: goal, State: "queued", Source: source, Created: time.Now().UTC(), Updated: time.Now().UTC(), Events: []Event{}}
	c.tasks[t.ID] = t
	if e := c.persistTasks(); e != nil {
		delete(c.tasks, t.ID)
		return Task{}, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.MaxSeconds)*time.Second)
	c.running[t.ID], c.devices[device] = cancel, t.ID
	c.wg.Add(1)
	go c.run(ctx, cancel, t.ID, s)
	return clone(t), nil
}
func (c *Control) Cancel(id string, source Source) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tasks[id]
	if !ok || !source.Admin && (source.Account != t.Source.Account || source.Thread != t.Source.Thread || source.Sender != t.Source.Sender) {
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
		c.mu.Unlock()
		if fn != nil {
			fn(t)
		}
	}()
	ctx = mcptools.WithSessionScope(ctx, id)
	b, e := c.factory(s)
	if e != nil {
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
			_ = c.update(id, "needs_review", "", "", "device_observation_unavailable", "")
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
		if step.Arguments == nil {
			step.Arguments = map[string]any{}
		}
		step.Arguments["device_id"], step.Arguments["observation_id"], step.Arguments["request_key"], step.Arguments["action"] = t.DeviceID, o.ID, id+"-step-"+hex.EncodeToString([]byte{byte(n)}), step.Action
		if e = c.update(id, "running", step.Action, "动作提交前检查点", "", o.ID); e != nil {
			return
		}
		args, _ := json.Marshal(step.Arguments)
		result, e := b.Call(ctx, "act", args)
		if e != nil {
			_ = c.update(id, "needs_review", "", "", "device_action_outcome_unknown", o.ID)
			return
		}
		var status struct {
			State string `json:"state"`
		}
		if json.Unmarshal([]byte(result), &status) != nil || status.State != "submitted" {
			_ = c.update(id, "needs_review", "", "", "device_action_outcome_unknown", o.ID)
			return
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

// StopAll checkpoints cancellation before stopping workers. It does not undo
// an ADB action already submitted. New tasks remain subject to Settings.Enabled.
func (c *Control) StopAll() error {
	for _, t := range c.List(nil) {
		if t.State == "queued" || t.State == "running" {
			if err := c.Cancel(t.ID, Source{Admin: true}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Control) Devices(ctx context.Context) (json.RawMessage, error) {
	s := c.Settings()
	if !s.Enabled {
		return nil, errors.New("device_control_disabled")
	}
	b, err := c.factory(s)
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
