package pluginruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"wechat-bot/internal/pluginhost"
)

// DependencyError contains identifiers and stable codes only, never provider
// configuration, filesystem paths or exception text from plugin processes.
type DependencyError struct {
	Plugin     string   `json:"plugin"`
	Dependency string   `json:"dependency"`
	Code       string   `json:"code"`
	Started    []string `json:"started,omitempty"`
}

func (e *DependencyError) Error() string {
	return fmt.Sprintf("plugin %s dependency %s: %s", e.Plugin, e.Dependency, e.Code)
}

// startPlan is read-only and traverses the entire installed graph before any
// process starts. Only the explicitly selected root may be enabled by Start;
// dependencies must already have the administrator's enabled intent.
func (c *Controller) startPlan(ctx context.Context, id string, revision int) ([]pluginhost.InstalledPackage, error) {
	state := map[string]int{}
	plan := []pluginhost.InstalledPackage{}
	var visit func(string) error
	visit = func(key string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if state[key] == 1 {
			return &DependencyError{Plugin: id, Dependency: key, Code: "dependency_cycle"}
		}
		if state[key] == 2 {
			return nil
		}
		i, err := c.store.Installed(ctx, key)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &DependencyError{Plugin: id, Dependency: key, Code: "dependency_missing"}
			}
			return err
		}
		if key == id && i.Revision != revision {
			return pluginhost.ErrRevision
		}
		if i.Removed {
			return &DependencyError{Plugin: id, Dependency: key, Code: "dependency_removed"}
		}
		if key != id && !i.Enabled {
			return &DependencyError{Plugin: id, Dependency: key, Code: "dependency_disabled"}
		}
		inv := pluginhost.Invocation{Account: c.store.Account(), Thread: "host", Requester: "administrator", Purpose: "check installed dependency", RequestKey: "dependency-check"}
		grant := pluginhost.Grant{Account: c.store.Account(), Plugin: key, Version: i.Package.Version, Permissions: i.Permissions}
		if err = pluginhost.Authorize(i.Package, grant, inv, i.Package.Permissions); err != nil {
			return &DependencyError{Plugin: id, Dependency: key, Code: "dependency_not_authorized"}
		}
		if err = c.store.CheckConfiguration(ctx, key); err != nil {
			return &DependencyError{Plugin: id, Dependency: key, Code: "dependency_configuration"}
		}
		state[key] = 1
		for _, dep := range i.Package.Dependencies {
			if err = visit(dep); err != nil {
				return err
			}
		}
		state[key] = 2
		plan = append(plan, i)
		return nil
	}
	if err := visit(id); err != nil {
		return nil, err
	}
	return plan, nil
}

// Reserve the whole graph atomically. Concurrent calls in an overlapping graph
// fail without holding a lock while waiting for external plugin processes.
func (c *Controller) beginPlan(plan []pluginhost.InstalledPackage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("plugin controller closed")
	}
	for _, i := range plan {
		if c.busy[i.Package.ID] {
			return &DependencyError{Plugin: plan[len(plan)-1].Package.ID, Dependency: i.Package.ID, Code: "dependency_busy"}
		}
	}
	for _, i := range plan {
		c.busy[i.Package.ID] = true
	}
	return nil
}
