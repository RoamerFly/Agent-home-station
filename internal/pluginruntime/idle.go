package pluginruntime

import (
	"context"
	"time"

	"wechat-bot/internal/pluginhost"
)

// controllerClock is intentionally local to the trusted runtime.  Plugin code
// has no route to control lifecycle time, while tests can advance a synthetic
// clock without sleeping.
type controllerClock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type wallClock struct{}

func (wallClock) Now() time.Time                             { return time.Now() }
func (wallClock) After(delay time.Duration) <-chan time.Time { return time.After(delay) }

func (c *Controller) notifyIdleLocked() {
	close(c.idleWake)
	c.idleWake = make(chan struct{})
}

func (c *Controller) idleLoop() {
	defer close(c.idleDone)
	// A wake is normally caused by one plugin's use.  Always recalculate the
	// earliest lease across every automatic session, rather than restarting a
	// single controller-wide full timeout and accidentally extending unrelated
	// sessions forever.
	recheckingDue := false
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return
		}
		delay, wake := c.nextIdleDelayLocked(recheckingDue), c.idleWake
		c.mu.Unlock()
		if delay < 0 {
			select {
			case <-c.idleStop:
				return
			case <-wake:
				recheckingDue = false
			}
			continue
		}
		if delay == 0 {
			c.reapIdle(context.Background())
			recheckingDue = true
			continue
		}
		select {
		case <-c.idleStop:
			return
		case <-wake:
			// A start, dispatch or policy change recomputes the lease below.
			recheckingDue = false
		case <-c.clock.After(delay):
			c.reapIdle(context.Background())
			recheckingDue = true
		}
	}
}

// nextIdleDelayLocked returns -1 while recycling is disabled.  Once a due
// session has been checked and was conservatively retained (for example, an
// unfinished remote job), it uses a bounded retry rather than a zero-duration
// loop.  Callers hold c.mu.
func (c *Controller) nextIdleDelayLocked(recheckingDue bool) time.Duration {
	if c.idleTimeout <= 0 {
		return -1
	}
	now := c.clock.Now()
	var earliest time.Duration
	found := false
	due := false
	for id, since := range c.automatic {
		if len(c.manual[id]) != 0 {
			continue
		}
		found = true
		deadline := since.Add(c.idleTimeout)
		if !deadline.After(now) {
			due = true
			continue
		}
		remaining := deadline.Sub(now)
		if earliest == 0 || remaining < earliest {
			earliest = remaining
		}
	}
	if !found {
		return c.idleTimeout
	}
	if due {
		if !recheckingDue {
			return 0
		}
		if c.idleTimeout < time.Minute {
			return c.idleTimeout
		}
		return time.Minute
	}
	return earliest
}

// recordGraphMode applies process ownership only after a complete graph is
// current.  An explicit Start pins every process needed by that root.  An
// EnsureStarted request makes unpinned graph members reclaimable and refreshes
// their lease, which also gives adjacent tasks a real process reuse window.
func (c *Controller) recordGraphMode(plan []pluginhost.InstalledPackage, root string, explicit bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	for _, item := range plan {
		id := item.Package.ID
		if explicit {
			if c.manual[id] == nil {
				c.manual[id] = map[string]bool{}
			}
			c.manual[id][root] = true
			delete(c.automatic, id)
			continue
		}
		if len(c.manual[id]) == 0 && c.host.Status(id).State == "running" {
			c.automatic[id] = now
		}
	}
	c.notifyIdleLocked()
}

func (c *Controller) releaseManualRootLocked(root string) {
	for id, roots := range c.manual {
		delete(roots, root)
		if len(roots) != 0 {
			continue
		}
		delete(c.manual, id)
		if c.host.Status(id).State == "running" {
			c.automatic[id] = c.clock.Now()
		}
	}
}

func (c *Controller) touch(id string) {
	c.mu.Lock()
	if !c.closed {
		if _, automatic := c.automatic[id]; automatic && len(c.manual[id]) == 0 {
			c.automatic[id] = c.clock.Now()
			c.notifyIdleLocked()
		}
	}
	c.mu.Unlock()
}

// reapIdle only targets sessions the runtime itself cold-started.  It reserves
// the same lifecycle slot used by calls/startup before checking durable jobs,
// so an old timer cannot stop a newer request which has already begun.
func (c *Controller) reapIdle(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	if c.closed || c.idleTimeout <= 0 {
		c.mu.Unlock()
		return
	}
	now, timeout := c.clock.Now(), c.idleTimeout
	candidates := make([]string, 0, len(c.automatic))
	for id, since := range c.automatic {
		if len(c.manual[id]) == 0 && !now.Before(since) && now.Sub(since) >= timeout {
			candidates = append(candidates, id)
		}
	}
	c.mu.Unlock()
	for _, id := range candidates {
		if ctx.Err() != nil {
			return
		}
		c.reapOne(ctx, id)
	}
}

func (c *Controller) reapOne(ctx context.Context, id string) {
	if err := c.begin(id); err != nil {
		return
	}
	defer func() {
		c.mu.Lock()
		delete(c.busy, id)
		c.notifyLocked()
		c.mu.Unlock()
	}()
	c.mu.Lock()
	if c.closed || c.idleTimeout <= 0 || len(c.manual[id]) != 0 {
		c.mu.Unlock()
		return
	}
	since, automatic := c.automatic[id]
	now, timeout, services := c.clock.Now(), c.idleTimeout, c.services
	c.mu.Unlock()
	if !automatic || now.Before(since) || now.Sub(since) < timeout {
		return
	}
	status := c.host.Status(id)
	if status.State != "running" || status.ActiveCalls != 0 {
		return
	}
	if services != nil && services.OwnerBusy(id) {
		return
	}
	// pending/submitting/running/needs_review all need recovery or a receipt
	// query.  Reclaiming their process could turn an uncertain side effect into
	// an unsafe replay after a later EnsureStarted.
	pending, err := c.store.HasUnfinishedJobs(ctx, id)
	if err != nil || pending {
		return
	}
	if c.host.Disable(id) != nil {
		// A running dependent or concurrent host transition wins.  The next idle
		// pass can retry after the dependent has been reclaimed; do not classify
		// this routine condition as a plugin failure.
		return
	}
	c.mu.Lock()
	delete(c.loaded, id)
	delete(c.automatic, id)
	c.notifyIdleLocked()
	c.mu.Unlock()
}
