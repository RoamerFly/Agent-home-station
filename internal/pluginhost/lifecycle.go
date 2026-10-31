package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

// Executor must enforce platform isolation before starting a local process.
// A plain exec.Command or legacy unrestricted MCP session is not an Executor.
type Executor interface {
	Start(context.Context, Package, Grant) (Session, error)
}
type Session interface {
	Health(context.Context) error
	Call(context.Context, Invocation, string, json.RawMessage) (json.RawMessage, error)
	Close() error
}
type Registration struct {
	Package Package
	Grant   Grant
	Enabled bool
}
type slot struct {
	registration Registration
	session      Session
	active       int
	stopping     bool
	closing      bool
	starting     bool
}

// Host is an account-bound runtime. Package approval/persistence belongs to the
// administrator control plane, not to tool calls. Sessions are never shared
// between accounts even when immutable package files are shared.
type Host struct {
	mu       sync.Mutex
	account  string
	executor Executor
	items    map[string]*slot
	closed   bool
}

type RuntimeStatus struct {
	State       string `json:"state"`
	ActiveCalls int    `json:"active_calls"`
	Registered  bool   `json:"registered"`
}

func (h *Host) Status(id string) RuntimeStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.items[id]
	if s == nil {
		return RuntimeStatus{State: "stopped"}
	}
	state := "stopped"
	if s.starting {
		state = "starting"
	} else if s.stopping {
		state = "stopping"
	} else if s.session != nil {
		state = "running"
		if live, ok := s.session.(interface{ Alive() bool }); ok && !live.Alive() {
			state = "failed"
		}
	}
	return RuntimeStatus{State: state, ActiveCalls: s.active, Registered: true}
}

func New(account string, executor Executor) (*Host, error) {
	if account == "" {
		return nil, errors.New("account required")
	}
	return &Host{account: account, executor: executor, items: map[string]*slot{}}, nil
}

func (h *Host) Register(r Registration) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errors.New("plugin host closed")
	}
	if r.Grant.Account != h.account || r.Grant.Plugin != r.Package.ID || r.Grant.Version != r.Package.Version {
		return errors.New("registration grant mismatch")
	}
	if old := h.items[r.Package.ID]; old != nil && (old.active > 0 || old.session != nil || old.starting || old.stopping) {
		return errors.New("stop plugin before replacing registration")
	}
	// Clone caller-owned arrays/maps so later config edits cannot mutate grants.
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var copy Registration
	if err = json.Unmarshal(b, &copy); err != nil {
		return err
	}
	h.items[r.Package.ID] = &slot{registration: copy}
	return nil
}

func (h *Host) Start(ctx context.Context, id string) error {
	h.mu.Lock()
	s := h.items[id]
	reject := func(err error) error { h.mu.Unlock(); return err }
	if h.closed || s == nil || !s.registration.Enabled || s.stopping || s.starting {
		return reject(errors.New("plugin unavailable or starting"))
	}
	if s.session != nil {
		return reject(nil)
	}
	p := s.registration.Package
	c := Invocation{Account: h.account, Thread: "host", Requester: "administrator", Purpose: "start installed plugin", RequestKey: "host-start"}
	if err := Authorize(p, s.registration.Grant, c, p.Permissions); err != nil {
		return reject(err)
	}
	for _, id := range p.Dependencies {
		d := h.items[id]
		if d == nil || !d.registration.Enabled || d.session == nil || d.stopping {
			return reject(errors.New("plugin dependency unavailable"))
		}
	}
	if h.executor == nil {
		return reject(errors.New("isolated plugin executor unavailable"))
	}
	s.starting = true
	h.mu.Unlock()
	session, err := h.executor.Start(ctx, p, s.registration.Grant)
	if err == nil && session == nil {
		err = errors.New("executor returned no session")
	}
	if err == nil {
		err = session.Health(ctx)
	}
	h.mu.Lock()
	if err == nil && (h.closed || !s.registration.Enabled) {
		err = errors.New("plugin host stopped during startup")
	}
	if err == nil {
		s.session = session
		s.starting = false
	}
	h.mu.Unlock()
	if err != nil {
		var closeErr error
		if session != nil {
			closeErr = session.Close()
		}
		h.mu.Lock()
		s.starting = false
		if closeErr != nil {
			// Do not start a replacement while termination is unconfirmed.
			s.session = session
			s.stopping = true
		}
		h.mu.Unlock()
		err = errors.Join(err, closeErr)
	}
	return err
}

// Call uses permissions declared by the trusted capability registry, not a
// list supplied by model arguments. Authorization is checked on every call.
func (h *Host) Call(ctx context.Context, id string, c Invocation, tool string, args json.RawMessage, permissions []string) (json.RawMessage, error) {
	h.mu.Lock()
	s := h.items[id]
	if h.closed || s == nil || !s.registration.Enabled || s.session == nil || s.stopping {
		h.mu.Unlock()
		return nil, errors.New("plugin unavailable")
	}
	if c.Account != h.account {
		h.mu.Unlock()
		return nil, errors.New("account mismatch")
	}
	if err := Authorize(s.registration.Package, s.registration.Grant, c, permissions); err != nil {
		h.mu.Unlock()
		return nil, err
	}
	s.active++
	session := s.session
	h.mu.Unlock()
	defer func() { h.mu.Lock(); s.active--; h.mu.Unlock() }()
	// An interrupted call is returned as-is, never replayed by the host.
	return session.Call(ctx, c, tool, args)
}
func (h *Host) Stop(id string) error {
	return h.stop(id, false)
}

func (h *Host) stop(id string, disable bool) error {
	h.mu.Lock()
	reject := func(err error) error { h.mu.Unlock(); return err }
	s := h.items[id]
	if s == nil {
		return reject(errors.New("plugin not installed"))
	}
	if s.active > 0 || s.starting {
		return reject(errors.New("plugin has active calls; drain first"))
	}
	if s.closing {
		return reject(errors.New("plugin close already in progress"))
	}
	for key, d := range h.items {
		if key == id || (d.session == nil && !d.starting) {
			continue
		}
		for _, dependency := range d.registration.Package.Dependencies {
			if dependency == id {
				return reject(errors.New("plugin has running dependents"))
			}
		}
	}
	if disable {
		// Reserve disabled state before releasing the mutex for external cleanup.
		s.registration.Enabled = false
	}
	if s.session == nil {
		return reject(nil)
	}
	s.stopping = true
	s.closing = true
	session := s.session
	h.mu.Unlock()
	return h.finishClose(s, session)
}
func (h *Host) Disable(id string) error {
	return h.stop(id, true)
}

// Never hold the host mutex while calling plugin code. An unresponsive plugin
// must not block calls or cancellation in unrelated plugin sessions.
func (h *Host) finishClose(s *slot, session Session) error {
	err := session.Close()
	h.mu.Lock()
	defer h.mu.Unlock()
	s.closing = false
	if err == nil {
		s.session = nil
		s.stopping = false
	}
	// A failed close remains unavailable, but Stop/Close may retry cleanup.
	return err
}
func (h *Host) Close() error {
	h.mu.Lock()
	h.closed = true
	var result error
	type pendingClose struct {
		slot    *slot
		session Session
	}
	var pending []pendingClose
	for _, s := range h.items {
		if s.active > 0 || s.starting || s.closing {
			result = errors.Join(result, errors.New("plugin has active calls"))
			continue
		}
		if s.session != nil {
			s.stopping = true
			s.closing = true
			pending = append(pending, pendingClose{s, s.session})
		}
	}
	h.mu.Unlock()
	for _, item := range pending {
		result = errors.Join(result, h.finishClose(item.slot, item.session))
	}
	return result
}
