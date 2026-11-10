package adapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"wechat-bot/internal/apphost"
)

func TestWorkerLifecycleDrainsBeforeCleanupAndRejectsLateWork(t *testing.T) {
	cleaned := make(chan struct{})
	lifecycle := NewWorkerLifecycle(func() { close(cleaned) })
	t.Cleanup(lifecycle.BeginShutdown)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	restored := make(chan string, 2)
	lifecycle.Restore(func(context.Context) ([]apphost.Connection, error) {
		return []apphost.Connection{{ID: "first"}, {ID: "second"}}, nil
	}, func(id string) {
		restored <- id
		if id == "first" {
			close(entered)
			<-release
		}
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("restore did not start")
	}
	lifecycle.BeginShutdown()
	if _, err := lifecycle.Start(func(context.Context, *WorkerRun) error {
		t.Error("late worker started")
		return nil
	}); !errors.Is(err, apphost.ErrLifecycleConflict) {
		t.Fatal("shutdown admitted a worker", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := lifecycle.CloseContext(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal("shutdown did not wait for restore", err)
	}
	select {
	case <-cleaned:
		t.Fatal("cleanup started before restore drained")
	default:
	}
	// Release the restoring account and wait for the cleanup callback to finish.
	release <- struct{}{}
	ctx, done := context.WithTimeout(t.Context(), 5*time.Second)
	defer done()
	if err := lifecycle.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	if id := <-restored; id != "first" || len(restored) != 0 {
		t.Fatal("restoration continued after shutdown", id)
	}
}

func TestWorkerDrainFencesOnlyTheSelectedAccount(t *testing.T) {
	lifecycle := NewWorkerLifecycle(nil)
	t.Cleanup(lifecycle.BeginShutdown)
	work := func(ctx context.Context, _ *WorkerRun) error {
		<-ctx.Done()
		return ctx.Err()
	}
	first, err := lifecycle.Start(work)
	if err != nil {
		t.Fatal(err)
	}
	second, err := lifecycle.Start(work)
	if err != nil {
		t.Fatal(err)
	}
	first.BlockCutover()
	if err := first.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !first.Blocked() || first.Status(false) != "cutover_blocked" || first.Status(true) != "" {
		t.Fatal("drained account lost its cutover or pause state")
	}
	if second.Blocked() || second.Status(false) != "waiting_login" {
		t.Fatal("draining one account changed the other account")
	}
	lifecycle.BeginShutdown()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := lifecycle.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-second.Done():
	default:
		t.Fatal("shutdown left the second account running")
	}
}
