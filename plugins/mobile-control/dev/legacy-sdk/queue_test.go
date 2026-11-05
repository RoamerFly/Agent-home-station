package mobile

import (
	"context"
	"testing"
)

func TestDeviceQueueRunsInOrderAndSkipsCancelled(t *testing.T) {
	entered, next, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	c, _ := mobileFixture(t, func(ctx context.Context, task Task, _ Observation) (Step, error) {
		switch task.Goal {
		case "first":
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return Step{}, ctx.Err()
			}
		case "second":
			close(next)
		case "cancelled":
			t.Error("cancelled queued task executed")
		}
		return Step{Action: "finish", Summary: task.Goal}, nil
	})
	first, err := c.Start(Source{Admin: true, MessageID: "first"}, "lab", "first")
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	second, err := c.Start(Source{Admin: true, MessageID: "second"}, "lab", "second")
	if err != nil {
		t.Fatal(err)
	}
	third, err := c.Start(Source{Admin: true, MessageID: "cancelled"}, "lab", "cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Cancel(third.ID, Source{Admin: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-next:
		t.Fatal("parallel same-device task")
	default:
	}
	close(release)
	waitState(t, c, first.ID, "completed")
	waitState(t, c, second.ID, "completed")
	waitState(t, c, third.ID, "cancelled")
}
func TestDeviceSettingsChangeCancelsRunningAndQueued(t *testing.T) {
	entered := make(chan struct{})
	c, _ := mobileFixture(t, func(ctx context.Context, _ Task, _ Observation) (Step, error) {
		close(entered)
		<-ctx.Done()
		return Step{}, ctx.Err()
	})
	first, err := c.Start(Source{Admin: true, MessageID: "first"}, "lab", "first")
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	second, err := c.Start(Source{Admin: true, MessageID: "second"}, "lab", "second")
	if err != nil {
		t.Fatal(err)
	}
	settings := c.Settings()
	settings.Enabled = false
	if err = c.Save(settings); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, first.ID, "cancelled")
	waitState(t, c, second.ID, "cancelled")
}
