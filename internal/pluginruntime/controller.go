package pluginruntime

import (
	"context"
	"errors"
	"reflect"
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
}

func InstalledExecutor(store *pluginhost.Store, options Options) *Executor {
	return &Executor{Resolve: func(ctx context.Context, p pluginhost.Package, g pluginhost.Grant) (LaunchSpec, error) {
		i, err := store.Installed(ctx, p.ID)
		if err != nil {
			return LaunchSpec{}, err
		}
		if !i.Enabled || i.Removed || store.Account() != g.Account || !reflect.DeepEqual(i.Package, p) || !reflect.DeepEqual(i.Permissions, g.Permissions) {
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
		return LaunchSpec{PackageDir: code, DataDir: data, Python: options.Python, Pythonw: options.Pythonw, WindowsHelper: options.WindowsHelper, RuntimeRoots: append([]string(nil), options.RuntimeRoots...), Configuration: &config}, nil
	}}
}

// Controller connects persisted administrative state to actual isolated
// sessions. Enabling does not submit tasks. Restart never replays tools.
type Controller struct {
	store    *pluginhost.Store
	host     *pluginhost.Host
	mu       sync.Mutex
	busy     map[string]bool
	failures map[string]string
	closed   bool
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
	return &Controller{store: store, host: h, busy: map[string]bool{}, failures: map[string]string{}}, nil
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
func (c *Controller) Start(ctx context.Context, id string, revision int) (err error) {
	if err = c.begin(id); err != nil {
		return err
	}
	defer func() { c.end(id, err) }()
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
	if c.host.Status(id).Registered {
		if err = c.host.Disable(id); err != nil {
			return err
		}
	}
	_, err = c.store.SetEnabled(ctx, id, revision, false)
	return err
}
func (c *Controller) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	// Keep Enabled as administrator intent across restarts. No process starts
	// automatically; explicit start may recover queries, never tool submissions.
	return c.host.Close()
}
