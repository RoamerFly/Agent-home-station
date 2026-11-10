package adapter

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
)

// WorkerSender grants the station host an account-scoped native transport only
// after checking the current roster. Every subsequent request rechecks ownership.
func (p *stationNativeProvider) WorkerSender(ctx context.Context, connectionID string) (*linuxnative.Sender, error) {
	sender, err := p.loginAccount(ctx, connectionID, false)
	if err != nil {
		return nil, err
	}
	sender.GuardRequests(p.verify)
	return sender, nil
}

// WorkerLifecycle owns admission and shutdown of the station's WeChat workers.
// The station supplies persisted policy and the work to run for each account.
type WorkerLifecycle struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	wg      sync.WaitGroup
	done    chan struct{}
	cleanup func()
}

func NewWorkerLifecycle(cleanup func()) *WorkerLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &WorkerLifecycle{ctx: ctx, cancel: cancel, done: make(chan struct{}), cleanup: cleanup}
}

func (l *WorkerLifecycle) Context() context.Context { return l.ctx }

func (l *WorkerLifecycle) start(work func()) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx.Err() != nil {
		return false
	}
	l.wg.Go(work)
	return true
}

func (l *WorkerLifecycle) Restore(connections func(context.Context) ([]apphost.Connection, error), restore func(string)) {
	l.start(func() {
		items, err := connections(l.ctx)
		if err != nil {
			return
		}
		for _, item := range items {
			if l.ctx.Err() != nil {
				return
			}
			restore(item.ID)
		}
	})
}

func (l *WorkerLifecycle) BeginShutdown() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx.Err() != nil {
		return
	}
	l.cancel()
	go func() {
		l.wg.Wait()
		if l.cleanup != nil {
			l.cleanup()
		}
		close(l.done)
	}()
}

func (l *WorkerLifecycle) CloseContext(ctx context.Context) error {
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WorkerRun is one account's in-memory run. Its submission fence is separate
// from the host's durable pause so a failed policy write can restore the fence.
type WorkerRun struct {
	state  atomic.Value
	block  atomic.Bool
	cancel context.CancelFunc
	done   chan struct{}
}

func (l *WorkerLifecycle) Start(work func(context.Context, *WorkerRun) error) (*WorkerRun, error) {
	ctx, cancel := context.WithCancel(l.ctx)
	run := &WorkerRun{cancel: cancel, done: make(chan struct{})}
	run.state.Store("waiting_login")
	if !l.start(func() {
		defer close(run.done)
		defer cancel()
		if err := work(ctx, run); err != nil && ctx.Err() == nil {
			run.state.Store("error")
		}
	}) {
		cancel()
		return nil, apphost.ErrLifecycleConflict
	}
	return run, nil
}

func (r *WorkerRun) Status(paused bool) string {
	select {
	case <-r.done:
		if paused {
			return ""
		}
	default:
		if r.Blocked() {
			return "stopping"
		}
	}
	return r.state.Load().(string)
}

func (r *WorkerRun) Blocked() bool                { return r.block.Load() }
func (r *WorkerRun) SetBlocked(blocked bool) bool { return r.block.Swap(blocked) }
func (r *WorkerRun) MarkRunning()                 { r.state.Store("running") }
func (r *WorkerRun) Done() <-chan struct{}        { return r.done }

func (r *WorkerRun) Cancel() {
	r.SetBlocked(true)
	r.cancel()
}

func (r *WorkerRun) BlockCutover() {
	r.SetBlocked(true)
	r.state.Store("cutover_blocked")
	r.cancel()
}

func (r *WorkerRun) Drain(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	r.Cancel()
	select {
	case <-r.Done():
		return nil
	case <-ctx.Done():
		return apphost.ErrLifecycleConflict
	}
}

func WaitWorkerLogin(ctx context.Context, native *linuxnative.Sender) error {
	for {
		statusCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		status, err := native.Status(statusCtx)
		cancel()
		if err == nil && status.Authenticated && (status.Connection == "online" || status.Connection == "connected") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
