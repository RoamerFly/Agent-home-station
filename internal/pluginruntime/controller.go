package pluginruntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"sync"
	"time"

	"wechat-bot/internal/pluginhost"
)

// Options come from deployment, never plugin/model-supplied tool arguments.
type Options struct {
	Python        string   `json:"python"`
	Pythonw       string   `json:"pythonw"`
	WindowsHelper string   `json:"windows_helper"`
	RuntimeRoots  []string `json:"runtime_roots"`
	// DeviceToolsRoots are deployment-owned host paths (for example ADB). They
	// are intentionally not copied into LaunchSpec/RuntimeRoots, so plugin
	// sandboxes never receive this host-only capability by configuration.
	DeviceToolsRoots []string `json:"device_tools_roots,omitempty"`
	// CatalogDirectory is a trusted deployment-owned directory containing
	// verified official plugin packages. It is never supplied by an API caller.
	CatalogDirectory string           `json:"catalog_directory"`
	ResourceRoots    ResourceResolver `json:"-"`
	HostMetadata     MetadataResolver `json:"-"`
}

func InstalledExecutor(store *pluginhost.Store, options Options) *Executor {
	return &Executor{Resolve: func(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) (LaunchSpec, error) {
		i, err := store.Installed(ctx, p.ID)
		if err != nil {
			return LaunchSpec{}, err
		}
		// Host.Register serializes its registration defensively. Optional manifest
		// collections can therefore round-trip from nil to empty without any
		// package, grant, or authorization change. Compare the canonical package
		// representation used by verification rather than rejecting that harmless
		// representation change.
		if !i.Enabled || i.Removed || store.Account() != g.Account || !sameVerifiedPackage(i.Package, p) || !reflect.DeepEqual(i.Permissions, g.Permissions) {
			return LaunchSpec{}, errors.New("installed plugin authorization changed")
		}
		code, err := store.PackageDirectory(i.Digest)
		if err != nil {
			return LaunchSpec{}, err
		}
		data, err := store.PrivateDirectory(ctx, p.ID)
		if err != nil {
			return LaunchSpec{}, err
		}
		config, err := store.ConfigurationForPlugin(ctx, p.ID)
		if err != nil {
			return LaunchSpec{}, err
		}
		resources := []ReadOnlyResource(nil)
		if options.ResourceRoots != nil {
			resources, err = options.ResourceRoots(ctx, p, g)
			if err != nil {
				return LaunchSpec{}, err
			}
		}
		metadata := map[string]json.RawMessage(nil)
		if options.HostMetadata != nil {
			metadata, err = options.HostMetadata(ctx, p, g)
			if err != nil {
				return LaunchSpec{}, err
			}
		}
		return LaunchSpec{PackageDir: code, DataDir: data, Python: options.Python, Pythonw: options.Pythonw, WindowsHelper: options.WindowsHelper, RuntimeRoots: append([]string(nil), options.RuntimeRoots...), Resources: append([]ReadOnlyResource(nil), resources...), HostMetadata: cloneMetadata(metadata), Configuration: &config}, nil
	}}
}

func cloneMetadata(metadata map[string]json.RawMessage) map[string]json.RawMessage {
	if len(metadata) == 0 {
		return nil
	}
	copy := make(map[string]json.RawMessage, len(metadata))
	for key, value := range metadata {
		copy[key] = append(json.RawMessage(nil), value...)
	}
	return copy
}

// Controller connects persisted administrative state to actual isolated
// sessions. Enabling does not submit tasks. Restart never replays tools.
type Controller struct {
	store    *pluginhost.Store
	host     *pluginhost.Host
	mu       sync.Mutex
	busy     map[string]bool
	changed  chan struct{}
	failures map[string]string
	loaded   map[string]loadedPlugin
	// automatic contains only sessions which were cold-started for on-demand
	// work.  Explicit Start pins a graph until it is stopped or explicitly
	// returned to on-demand mode; status and health reads never renew this lease.
	automatic   map[string]time.Time
	manual      map[string]map[string]bool // plugin -> explicit root pins
	idleTimeout time.Duration
	clock       controllerClock
	idleWake    chan struct{}
	idleStop    chan struct{}
	idleDone    chan struct{}
	closed      bool
	services    *ManagedServices
}

// loadedPlugin is the exact persistent identity that the currently running
// process was launched from. Compatibility is insufficient here: a changed
// digest or configuration requires an explicit stop/restart before dispatch.
type loadedPlugin struct {
	digest, configRevision string
	revision               int
	packageSnapshot        pluginhost.Package
}
type Status struct {
	pluginhost.RuntimeStatus
	ErrorCode    string `json:"error_code,omitempty"`
	AutoManaged  bool   `json:"auto_managed,omitempty"`
	IdleDeadline string `json:"idle_deadline,omitempty"`
}

// EnsureStartedError has a stable, model-safe failure description. It never
// contains plugin process output, paths, provider responses, or credentials.
type EnsureStartedError struct {
	Code    string
	Message string
}

func (e *EnsureStartedError) Error() string { return e.Message }
func (e *EnsureStartedError) SafeToolFailure() (string, string) {
	return e.Code, e.Message
}

func NewController(store *pluginhost.Store, executor pluginhost.Executor) (*Controller, error) {
	return newControllerWithIdleLoop(store, executor, wallClock{}, true)
}

// newControllerWithClock keeps clock control private to the runtime package so
// production callers cannot supply a model/plugin-controlled clock.  Tests use
// it to exercise expiry without sleeping.  Tests drive reapIdle explicitly so
// a synthetic clock cannot race a real background wake.
func newControllerWithClock(store *pluginhost.Store, executor pluginhost.Executor, clock controllerClock) (*Controller, error) {
	return newControllerWithIdleLoop(store, executor, clock, false)
}

func newControllerWithIdleLoop(store *pluginhost.Store, executor pluginhost.Executor, clock controllerClock, runIdleLoop bool) (*Controller, error) {
	h, err := pluginhost.New(store.Account(), executor)
	if err != nil {
		return nil, err
	}
	if clock == nil {
		clock = wallClock{}
	}
	c := &Controller{store: store, host: h, busy: map[string]bool{}, changed: make(chan struct{}), failures: map[string]string{}, loaded: map[string]loadedPlugin{}, automatic: map[string]time.Time{}, manual: map[string]map[string]bool{}, idleTimeout: 10 * time.Minute, clock: clock, idleWake: make(chan struct{}), idleStop: make(chan struct{}), idleDone: make(chan struct{})}
	if runIdleLoop {
		go c.idleLoop()
	} else {
		close(c.idleDone)
	}
	return c, nil
}

// AttachManagedServices binds the host-owned resource registry once. It does
// not expose service descriptors or process-launch rights to plugin code.
func (c *Controller) AttachManagedServices(services *ManagedServices) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || services == nil || c.services != nil && c.services != services {
		return errors.New("plugin service registry unavailable or already bound")
	}
	c.services = services
	return nil
}

// notifyLocked wakes callers waiting to reserve a plugin invocation.  The
// channel is replaced after close so a waiter always observes one concrete
// state transition instead of polling a mutable flag.
func (c *Controller) notifyLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}
func (c *Controller) begin(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.busy[id] {
		return errors.New("plugin lifecycle operation unavailable")
	}
	c.busy[id] = true
	return nil
}
func (c *Controller) end(id string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.busy, id)
	c.notifyLocked()
	if err != nil {
		c.failures[id] = "runtime_operation_failed"
	} else {
		delete(c.failures, id)
	}
}
func (c *Controller) Status(id string) Status {
	r := c.host.Status(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.failures[id]
	if r.State == "stopped" && err != "" {
		r.State = "failed"
	}
	status := Status{RuntimeStatus: r, ErrorCode: err}
	if since, ok := c.automatic[id]; ok && r.State == "running" && c.idleTimeout > 0 {
		status.AutoManaged = true
		status.IdleDeadline = since.Add(c.idleTimeout).UTC().Format(time.RFC3339Nano)
	}
	return status
}

// SetIdleTimeout changes automatic session recycling immediately.  Zero (and a
// negative value) disables recycling.  It never enables, disables or starts a
// plugin; explicit administrator starts remain resident until stopped or
// EnableOnDemand returns that graph to on-demand ownership.
func (c *Controller) SetIdleTimeout(timeout time.Duration) {
	if timeout < 0 {
		timeout = 0
	}
	c.mu.Lock()
	if !c.closed {
		c.idleTimeout = timeout
		c.notifyIdleLocked()
	}
	c.mu.Unlock()
}

// EnableOnDemand records administrator enable intent without launching a
// process.  If a graph was previously held by this root's explicit Start, it
// removes only that root's pins; independently pinned shared dependencies keep
// running.  A next tool request uses EnsureStarted to cold-start as needed.
func (c *Controller) EnableOnDemand(ctx context.Context, id string, revision int) (err error) {
	plan, err := c.startPlan(ctx, id, revision)
	if err != nil {
		return err
	}
	if err = c.beginPlan(plan); err != nil {
		return err
	}
	defer func() {
		for _, item := range plan {
			c.end(item.Package.ID, err)
		}
	}()
	root, err := c.store.Installed(ctx, id)
	if err != nil {
		return err
	}
	if root.Revision != revision || root.Removed {
		return pluginhost.ErrRevision
	}
	if !root.Enabled {
		if _, err = c.store.SetEnabled(ctx, id, revision, true); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.releaseManualRootLocked(id)
	for _, item := range plan {
		pluginID := item.Package.ID
		if len(c.manual[pluginID]) == 0 && c.host.Status(pluginID).State == "running" {
			c.automatic[pluginID] = c.clock.Now()
		}
	}
	c.notifyIdleLocked()
	c.mu.Unlock()
	return nil
}

// Closed reports host shutdown without changing persisted administrator intent.
func (c *Controller) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Start is the administrator action. It may enable the requested root before
// starting it. Dependencies must already be enabled.
func (c *Controller) Start(ctx context.Context, id string, revision int) error {
	return c.startGraph(ctx, id, revision, true, false)
}

// EnsureStarted starts an already enabled plugin and its already enabled
// dependencies. It never changes administrator enable intent or dispatches a
// tool. A fully current running graph returns immediately without waiting on
// a nested invocation that happens to hold the same lifecycle reservation.
func (c *Controller) EnsureStarted(ctx context.Context, id string, revision int) error {
	if err := c.enabledRoot(ctx, id, revision); err != nil {
		return ensureStartedFailure(err)
	}
	if c.host.Status(id).State == "running" {
		err := c.loadedGraphCurrent(ctx, id)
		if err == nil {
			return nil
		}
		var dependency *DependencyError
		if !errors.As(err, &dependency) || dependency.Code != "dependency_not_running" {
			return ensureStartedFailure(err)
		}
	}
	return ensureStartedFailure(c.startGraph(ctx, id, revision, false, true))
}

func (c *Controller) enabledRoot(ctx context.Context, id string, revision int) error {
	root, err := c.store.Installed(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &DependencyError{Plugin: id, Dependency: id, Code: "dependency_missing"}
		}
		return err
	}
	if root.Revision != revision {
		return pluginhost.ErrRevision
	}
	if root.Removed {
		return &DependencyError{Plugin: id, Dependency: id, Code: "dependency_removed"}
	}
	if !root.Enabled {
		return &EnsureStartedError{Code: "plugin_not_enabled", Message: "插件尚未启用，不能自动启动"}
	}
	return nil
}

// startGraph is the single lifecycle execution path. Administrators use a
// fail-fast reservation and may enable only the selected root. Automatic
// recovery waits for an overlapping graph and requires every package to have
// retained its enabled intent.
func (c *Controller) startGraph(ctx context.Context, id string, revision int, enableRoot, wait bool) (err error) {
	if !enableRoot {
		if err = c.enabledRoot(ctx, id, revision); err != nil {
			return err
		}
	}
	plan, err := c.startPlan(ctx, id, revision)
	if err != nil {
		return err
	}
	if wait {
		err = c.waitBeginPlan(ctx, plan)
	} else {
		err = c.beginPlan(plan)
	}
	if err != nil {
		return err
	}
	operationErrors := map[string]error{}
	defer func() {
		for _, item := range plan {
			if item.Package.ID == id {
				c.end(id, err)
			} else {
				c.end(item.Package.ID, operationErrors[item.Package.ID])
			}
		}
	}()
	for _, expected := range plan {
		current, readErr := c.store.Installed(ctx, expected.Package.ID)
		if readErr != nil || current.Revision != expected.Revision || current.Removed || (!enableRoot && !current.Enabled) || (expected.Package.ID != id && !current.Enabled) {
			return &DependencyError{Plugin: id, Dependency: expected.Package.ID, Code: "dependency_changed"}
		}
	}
	started := []string{}
	for _, item := range plan {
		wasRunning := c.host.Status(item.Package.ID).State == "running"
		canEnable := enableRoot && item.Package.ID == id
		if err = c.startOne(ctx, item.Package.ID, item.Revision, canEnable); err != nil {
			operationErrors[item.Package.ID] = err
			if len(plan) == 1 {
				return err
			}
			return &DependencyError{Plugin: id, Dependency: item.Package.ID, Code: "dependency_start_failed", Started: started}
		}
		if !wasRunning {
			started = append(started, item.Package.ID)
		}
	}
	if err = c.loadedGraphCurrent(ctx, id); err != nil {
		return err
	}
	c.recordGraphMode(plan, id, enableRoot)
	return nil
}

func ensureStartedFailure(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var safe *EnsureStartedError
	if errors.As(err, &safe) {
		return safe
	}
	var dependency *DependencyError
	if errors.As(err, &dependency) {
		switch dependency.Code {
		case "dependency_missing", "dependency_removed":
			return &EnsureStartedError{Code: "plugin_dependency_missing", Message: "所需依赖插件未安装或已移除"}
		case "dependency_disabled":
			return &EnsureStartedError{Code: "plugin_dependency_not_enabled", Message: "所需依赖插件尚未启用"}
		case "dependency_not_authorized":
			return &EnsureStartedError{Code: "plugin_authorization_required", Message: "插件尚未获得所需授权"}
		case "dependency_configuration":
			return &EnsureStartedError{Code: "plugin_configuration_required", Message: "插件或其依赖尚未完成配置"}
		case "dependency_changed", "dependency_restart_required":
			return &EnsureStartedError{Code: "plugin_configuration_changed", Message: "插件配置或版本已变化，需要由管理页重新启动"}
		case "dependency_cycle":
			return &EnsureStartedError{Code: "plugin_dependency_invalid", Message: "插件依赖声明存在循环"}
		case "dependency_start_failed":
			return &EnsureStartedError{Code: "plugin_cold_start_failed", Message: "插件冷启动未完成，可检查插件状态后重试"}
		}
	}
	if errors.Is(err, pluginhost.ErrRevision) {
		return &EnsureStartedError{Code: "plugin_configuration_changed", Message: "插件配置或版本已变化，需要由管理页重新启动"}
	}
	return &EnsureStartedError{Code: "plugin_cold_start_failed", Message: "插件冷启动未完成，可检查插件状态后重试"}
}

// The caller owns lifecycle reservations for this plugin and its dependencies.
// enable permits the administrative root action to create enabled intent. It
// is false for dependencies and for automatic recovery.
func (c *Controller) startOne(ctx context.Context, id string, revision int, enable bool) (err error) {
	i, err := c.store.Installed(ctx, id)
	if err != nil {
		return err
	}
	if i.Revision != revision || i.Removed {
		return pluginhost.ErrRevision
	}
	enabledByThisCall := false
	if !i.Enabled {
		if !enable {
			return &EnsureStartedError{Code: "plugin_not_enabled", Message: "插件尚未启用，不能自动启动"}
		}
		i, err = c.store.SetEnabled(ctx, id, revision, true)
		if err != nil {
			return err
		}
		enabledByThisCall = true
	}
	state := c.host.Status(id)
	if state.State == "running" {
		return nil
	}
	if state.State == "failed" {
		// This only clears the host registration. A failed session with an
		// active call cannot be closed, so the host rejects the recovery.
		if err = c.host.Disable(id); err != nil {
			return err
		}
		state = c.host.Status(id)
	}
	if state.State != "stopped" {
		return errors.New("stop failed or interrupted session before restarting")
	}
	config, err := c.store.Configuration(ctx, id)
	if err != nil {
		return err
	}
	r := pluginhost.Registration{Package: i.Package, Grant: pluginhost.Grant{Account: c.store.Account(), Plugin: id, Version: i.Package.Version, Permissions: i.Permissions}, Enabled: true}
	err = c.host.Register(r)
	if err == nil {
		err = c.host.Start(ctx, id)
	}
	if err != nil {
		// Cleanup only affects this attempted session. Automatic recovery keeps
		// persisted intent, while an administrative enable rolls back its own
		// reservation when startup never completed.
		if c.host.Status(id).Registered {
			if closeErr := c.host.Disable(id); closeErr != nil {
				return errors.Join(err, closeErr)
			}
		}
		if enabledByThisCall {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, releaseErr := c.store.SetEnabled(cleanup, id, i.Revision, false)
			return errors.Join(err, releaseErr)
		}
		return err
	}
	c.mu.Lock()
	c.loaded[id] = loadedPlugin{digest: i.Digest, revision: i.Revision, configRevision: strconv.Itoa(config.Revision), packageSnapshot: i.Package}
	c.mu.Unlock()
	return nil
}
func (c *Controller) Stop(ctx context.Context, id string, revision int) (err error) {
	if err = c.begin(id); err != nil {
		return err
	}
	defer func() { c.end(id, err) }()
	i, err := c.store.Installed(ctx, id)
	if err != nil {
		return err
	}
	if i.Revision != revision {
		return pluginhost.ErrRevision
	}
	c.mu.Lock()
	services := c.services
	c.mu.Unlock()
	if services != nil {
		if err = services.StopOwner(ctx, id); err != nil {
			return err
		}
	}
	if c.host.Status(id).Registered {
		if err = c.host.Disable(id); err != nil {
			return err
		}
	}
	_, err = c.store.SetEnabled(ctx, id, revision, false)
	if err == nil {
		c.mu.Lock()
		delete(c.loaded, id)
		delete(c.automatic, id)
		c.releaseManualRootLocked(id)
		c.notifyIdleLocked()
		c.mu.Unlock()
	}
	return err
}
func (c *Controller) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	services := c.services
	c.loaded = map[string]loadedPlugin{}
	c.notifyLocked()
	close(c.idleStop)
	c.mu.Unlock()
	// Let a reaper which already reserved a session finish its exact close
	// before Host.Close walks remaining slots.  This avoids two concurrent Close
	// calls targeting the same plugin process during desktop shutdown.
	<-c.idleDone
	// Keep Enabled as administrator intent across restarts. No process starts
	// automatically; explicit start may recover queries, never tool submissions.
	err := c.host.Close()
	if services != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 56*time.Second)
		defer cancel()
		err = errors.Join(err, services.Close(cleanup))
	}
	return err
}
