package mobile

import (
	"context"
	"errors"
	"testing"
)

func TestBrokerFactoryReceivesCurrentTaskAndSettings(t *testing.T) {
	c, bridge := mobileFixture(t, func(context.Context, Task, Observation) (Step, error) {
		return Step{Action: "finish", Summary: "done"}, nil
	})
	var gotTask Task
	var gotSettings Settings
	if err := c.SetBrokerFactory(func(_ context.Context, task Task, settings Settings) (Bridge, error) {
		gotTask, gotSettings = task, settings
		return bridge, nil
	}); err != nil {
		t.Fatal(err)
	}
	source := Source{Admin: true, Account: "account", Thread: "thread", Sender: "owner", MessageID: "bridge-context"}
	task, err := c.Start(source, "lab", "inspect")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, task.ID, "completed")
	if gotTask.ID != task.ID || gotTask.Source != source || gotSettings.Revision != c.Settings().Revision || !gotSettings.Enabled {
		t.Fatalf("factory context = task=%#v settings=%#v", gotTask, gotSettings)
	}
}

func TestBrokerFactoryRejectsRevokedOrWrongTaskScopeBeforeOpen(t *testing.T) {
	c, _ := mobileFixture(t, func(context.Context, Task, Observation) (Step, error) { return Step{Action: "finish"}, nil })
	c.SetAuthorizer(func(source Source) bool {
		return source.Admin && source.Account == "allowed" && source.Thread == "room"
	})
	opened := 0
	if err := c.SetBrokerFactory(func(context.Context, Task, Settings) (Bridge, error) {
		opened++
		return &fakeBridge{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.tasks["wrong-scope"] = Task{ID: "wrong-scope", State: "queued", DeviceID: "lab", Source: Source{Admin: true, Account: "other", Thread: "room", MessageID: "source"}}
	c.mu.Unlock()
	if _, err := c.openTaskBridge(context.Background(), "wrong-scope"); !errors.Is(err, errPermissionChanged) {
		t.Fatalf("open error = %v, want permission change", err)
	}
	if opened != 0 {
		t.Fatalf("bridge opened for rejected source: %d", opened)
	}
}

func TestBrokerFactoryFailureNeverFallsBackToLegacy(t *testing.T) {
	c, _ := mobileFixture(t, func(context.Context, Task, Observation) (Step, error) { return Step{Action: "finish"}, nil })
	legacyCalls := 0
	c.factory = func(Settings) (Bridge, error) {
		legacyCalls++
		return &fakeBridge{}, nil
	}
	if err := c.SetBrokerFactory(func(context.Context, Task, Settings) (Bridge, error) {
		return nil, errors.New("plugin unavailable")
	}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.tasks["plugin-failure"] = Task{ID: "plugin-failure", State: "queued", DeviceID: "lab", Source: Source{Admin: true, MessageID: "source"}}
	c.mu.Unlock()
	if _, err := c.openTaskBridge(context.Background(), "plugin-failure"); err == nil {
		t.Fatal("plugin factory failure unexpectedly succeeded")
	}
	if legacyCalls != 0 {
		t.Fatalf("legacy bridge was used after plugin failure: %d", legacyCalls)
	}
	if err := c.SetBrokerFactory(func(context.Context, Task, Settings) (Bridge, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.openTaskBridge(context.Background(), "plugin-failure"); err == nil {
		t.Fatal("nil plugin bridge unexpectedly succeeded")
	}
	if legacyCalls != 0 {
		t.Fatalf("legacy bridge was used after nil plugin bridge: %d", legacyCalls)
	}
}

func TestBrokerFactoryDevicesUsesUnscopedAdminTask(t *testing.T) {
	c, bridge := mobileFixture(t, func(context.Context, Task, Observation) (Step, error) { return Step{Action: "finish"}, nil })
	c.SetAuthorizer(func(source Source) bool {
		return source.Admin && source.Account == "" && source.Thread == "" && source.Sender == ""
	})
	called := 0
	if err := c.SetBrokerFactory(func(_ context.Context, task Task, settings Settings) (Bridge, error) {
		called++
		if task.ID != "" || task.Source != (Source{Admin: true}) || !settings.Enabled {
			t.Fatalf("management context = task=%#v settings=%#v", task, settings)
		}
		return bridge, nil
	}); err != nil {
		t.Fatal(err)
	}
	devices, err := c.Devices(context.Background())
	if err != nil || string(devices) == "" || called != 1 {
		t.Fatalf("Devices = %q, %v; calls=%d", devices, err, called)
	}
}

func TestSetBrokerFactoryRejectsRunningTask(t *testing.T) {
	entered := make(chan struct{})
	c, _ := mobileFixture(t, func(ctx context.Context, _ Task, _ Observation) (Step, error) {
		close(entered)
		<-ctx.Done()
		return Step{}, ctx.Err()
	})
	task, err := c.Start(Source{Admin: true, MessageID: "running-factory"}, "lab", "wait")
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if err = c.SetBrokerFactory(func(context.Context, Task, Settings) (Bridge, error) { return &fakeBridge{}, nil }); err == nil {
		t.Fatal("factory replacement accepted while task was running")
	}
	if err = c.Cancel(task.ID, Source{Admin: true, MessageID: "running-factory"}); err != nil {
		t.Fatal(err)
	}
}
