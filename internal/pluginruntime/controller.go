package pluginruntime

import (
	"context"
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
	closed   bool
	services *ManagedServices
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
	ErrorCode string `json:"error_code,omitempty"`
}

func NewController(store *pluginhost.Store, executor pluginhost.Executor) (*Controller, error) {
	h, err := pluginhost.New(store.Account(), executor)
	if err != nil {
		return nil, err
	}
	return &Controller{store: store, host: h, busy: map[string]bool{}, changed: make(chan struct{}), failures: map[string]string{}, loaded: map[string]loadedPlugin{}}, nil
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
	return Status{r, err}
}

// Closed reports host shutdown without changing persisted administrator intent.
func (c *Controller) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *Controller) Start(ctx context.Context, id string, revision int) (err error) {
	plan, err := c.startPlan(ctx, id, revision)
	if err != nil {
		return err
	}
	if err = c.beginPlan(plan); err != nil {
		return err
	}
	operationErrors := map[string]error{}
	defer func() {
		for _, i := range plan {
			if i.Package.ID == id {
				c.end(id, err)
			} else {
				c.end(i.Package.ID, operationErrors[i.Package.ID])
			}
		}
	}()
	// Recheck every snapshot before starting even the first dependency.
	for _, expected := range plan {
		current, e := c.store.Installed(ctx, expected.Package.ID)
		if e != nil || current.Revision != expected.Revision || current.Removed || (expected.Package.ID != id && !current.Enabled) {
			return &DependencyError{Plugin: id, Dependency: expected.Package.ID, Code: "dependency_changed"}
		}
	}
	started := []string{}
	for _, i := range plan {
		wasRunning := c.host.Status(i.Package.ID).State == "running"
		if err = c.startOne(ctx, i.Package.ID, i.Revision); err != nil {
			operationErrors[i.Package.ID] = err
			if len(plan) == 1 {
				return err
			}
			return &DependencyError{Plugin: id, Dependency: i.Package.ID, Code: "dependency_start_failed", Started: started}
		}
		if !wasRunning {
			started = append(started, i.Package.ID)
		}
	}
	return nil
}

// The caller owns lifecycle reservations for this plugin and its dependencies.
func (c *Controller) startOne(ctx context.Context, id string, revision int) (err error) {
	i, err := c.store.Installed(ctx, id)
	if err != nil {
		return err
	}
	if i.Revision != revision || i.Removed {
		return pluginhost.ErrRevision
	}
	state := c.host.Status(id)
	if state.State == "running" {
		return nil
	}
	if state.State != "stopped" {
		return errors.New("stop failed or interrupted session before restarting")
	}
	i, err = c.store.SetEnabled(ctx, id, revision, true)
	if err != nil {
		return err
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
		// Cleanup must survive cancellation of the initiating HTTP request.
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if c.host.Status(id).Registered {
			if closeErr := c.host.Disable(id); closeErr != nil {
				return errors.Join(err, closeErr)
			}
		}
		_, releaseErr := c.store.SetEnabled(cleanup, id, i.Revision, false)
		return errors.Join(err, releaseErr)
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
		c.mu.Unlock()
	}
	return err
}
func (c *Controller) Close() error {
	c.mu.Lock()
	c.closed = true
	services := c.services
	c.loaded = map[string]loadedPlugin{}
	c.notifyLocked()
	c.mu.Unlock()
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
