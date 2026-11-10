package adapter

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
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

func TestWorkerQueuePreservesScopedAdmission(t *testing.T) {
	const room = "fixture-room@chatroom"
	now := time.Now().UTC()
	grant := now.Add(-time.Minute)
	run := &WorkerRun{}
	calls := 0
	queue := WorkerQueue{
		Groups: GroupPolicy{
			Replies:     map[string]time.Time{room: grant},
			Attachments: map[string]time.Time{room: grant},
			Rules:       map[string]GroupRule{room: {Mode: "social", SummarizeSince: now}},
		},
		AllowMediaReplies: true,
		ReadGroupShare: func(context.Context, companion.Input, linuxnative.Message) (string, companion.MediaObservationEvidence, error) {
			calls++
			return "fixture observed", companion.MediaObservationEvidence{}, nil
		},
	}
	character, config := run.queueConfig("one", queue, func() error { return nil })
	if !slices.Equal(config.AllowedThreads, []string{room}) || config.GroupRules[room].Mode != "social" ||
		!character.Permissions.ObserveGroups || !character.Permissions.SendFile || config.Options.RandomDelay {
		t.Fatalf("WeChat defaults or scoped policy changed: %+v", character.Permissions)
	}
	msg := linuxnative.Message{ThreadID: room, AccountID: "one", Direction: "incoming", CreatedAt: now.Format(time.RFC3339Nano), Type: 49}
	file := msg
	file.Body = "<msg><appmsg><type>6</type><title>fixture.pdf</title></appmsg></msg>"
	if !config.OnDemandAttachment(file) {
		t.Fatal("granted inbound file was not admitted on demand")
	}
	file.CreatedAt = grant.Add(-time.Second).Format(time.RFC3339Nano)
	if config.OnDemandAttachment(file) {
		t.Fatal("pre-grant attachment was admitted")
	}
	kind := 51
	msg.AppMessageType = &kind
	if !config.PreferredGroupShare(msg) {
		t.Fatal("granted Channels share was not admitted")
	}
	if text, _, err := config.ReadPreferredGroupShare(t.Context(), companion.Input{}, msg); err != nil || text != "fixture observed" {
		t.Fatal(text, err)
	}
	foreign := msg
	foreign.AccountID = "two"
	if config.PreferredGroupShare(foreign) {
		t.Fatal("foreign connection reached the share reader")
	}
	run.SetBlocked(true)
	if text, _, err := config.ReadPreferredGroupShare(t.Context(), companion.Input{}, msg); err == nil || text != "" || calls != 1 {
		t.Fatalf("paused worker reached the share reader: %q %v", text, err)
	}
}

func TestWorkerQueueRechecksActivationInsideDeliveryFence(t *testing.T) {
	run := &WorkerRun{}
	revoked := false
	queue := WorkerQueue{
		WithDeliverySubmit: func(_ context.Context, _ companion.Input, submit func() error) error {
			revoked = true
			return submit()
		},
	}
	_, config := run.queueConfig("one", queue, func() error {
		if revoked {
			return apphost.ErrLifecycleCutover
		}
		return nil
	})
	if err := config.BeforeSubmit(t.Context()); err != nil {
		t.Fatal(err)
	}
	err := config.WithDeliverySubmit(t.Context(), companion.Input{}, func() error {
		t.Fatal("revoked activation reached native submit")
		return nil
	})
	if !errors.Is(err, apphost.ErrLifecycleCutover) {
		t.Fatalf("wanted cutover error, got %v", err)
	}
}
