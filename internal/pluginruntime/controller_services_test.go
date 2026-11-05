package pluginruntime

import (
	"context"
	"os"
	"sync/atomic"
	"testing"

	"wechat-bot/internal/pluginhost"
)

type controllerServiceSession struct {
	invocationSession
	onClose func() error
}

func (s *controllerServiceSession) Close() error {
	if s.onClose != nil {
		return s.onClose()
	}
	return nil
}

// The Controller uses an in-memory synthetic plugin session. Its managed
// services are real copies of this Go test binary in isolated temporary dirs.
func TestControllerManagedServicesStopChecksBusyBeforeDisablingOwner(t *testing.T) {
	store, installed := installedFixture(t)
	registry := &ManagedServices{}
	var closed atomic.Int32
	session := &controllerServiceSession{onClose: func() error {
		closed.Add(1)
		if registry.Status("owner-idle").Running || registry.Status("owner-busy").Running {
			t.Error("plugin session disabled before its owned supervisors stopped")
		}
		return nil
	}}
	controller, err := NewController(store, startFunc(func(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
		return session, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if err = controller.AttachManagedServices(registry); err != nil {
		t.Fatal(err)
	}
	if err = controller.Start(t.Context(), installed.Package.ID, installed.Revision); err != nil {
		t.Fatal(err)
	}
	current, err := store.Installed(t.Context(), installed.Package.ID)
	if err != nil || !current.Enabled {
		t.Fatal("synthetic plugin not enabled", err)
	}
	start := func(id, owner string) ManagedServiceSpec {
		spec := managedFixtureSpec(t, "wait")
		spec.OwnerPlugin = owner
		if _, err := registry.Start(t.Context(), id, spec, fixtureAdmission); err != nil {
			t.Fatal(err)
		}
		return spec
	}
	start("owner-idle", installed.Package.ID)
	busy := start("owner-busy", installed.Package.ID)
	other := start("other-busy", "unrelated-plugin")
	for _, path := range []string{busy.PendingJobsPath, other.PendingJobsPath} {
		if err := os.WriteFile(path, []byte(`{"job":{"state":"queued"}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = controller.Stop(t.Context(), installed.Package.ID, current.Revision+1); err == nil {
		t.Fatal("stale plugin stop admitted")
	}
	if err = controller.Stop(t.Context(), installed.Package.ID, current.Revision); err == nil {
		t.Fatal("busy owner disabled")
	}
	after, err := store.Installed(t.Context(), installed.Package.ID)
	if err != nil || !after.Enabled || after.Revision != current.Revision || closed.Load() != 0 {
		t.Fatal("busy refusal changed plugin state", after, closed.Load(), err)
	}
	if !registry.Status("owner-idle").Running || !registry.Status("owner-busy").Running || !registry.Status("other-busy").Running {
		t.Fatal("busy refusal partially stopped resources")
	}
	if err = os.WriteFile(busy.PendingJobsPath, []byte(`{"job":{"state":"completed"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = controller.Stop(t.Context(), installed.Package.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
	after, err = store.Installed(t.Context(), installed.Package.ID)
	if err != nil || after.Enabled || closed.Load() != 1 {
		t.Fatal("successful stop did not disable plugin", after, closed.Load(), err)
	}
	if registry.Status("owner-idle").Running || registry.Status("owner-busy").Running || !registry.Status("other-busy").Running || !registry.OwnerBusy("unrelated-plugin") {
		t.Fatal("stop crossed owner boundary or retained owned resources")
	}
}

func TestControllerManagedServicesCloseReclaimsEveryOwnerDespitePendingJobs(t *testing.T) {
	store, installed := installedFixture(t)
	controller, err := NewController(store, startFunc(func(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
		return &invocationSession{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	registry := &ManagedServices{}
	if err = controller.AttachManagedServices(registry); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetEnabled(t.Context(), installed.Package.ID, installed.Revision, true); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{installed.Package.ID, "unrelated-plugin"} {
		spec := managedFixtureSpec(t, "wait")
		spec.OwnerPlugin = owner
		if _, err := registry.Start(t.Context(), owner, spec, fixtureAdmission); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(spec.PendingJobsPath, []byte(`{"job":{"state":"running"}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = controller.Close(); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{installed.Package.ID, "unrelated-plugin"} {
		if registry.Status(owner).Running || registry.OwnerBusy(owner) {
			t.Fatal("controller close retained owned supervisor", owner)
		}
	}
	current, err := store.Installed(t.Context(), installed.Package.ID)
	if err != nil || !current.Enabled {
		t.Fatal("host shutdown erased persistent administrator enable intent", err)
	}
	if _, err = registry.Start(t.Context(), "after-close", managedFixtureSpec(t, "wait"), fixtureAdmission); err == nil {
		t.Fatal("closed controller resource registry restarted")
	}
}

func TestControllerManagedServicesAttachmentCannotBeSwappedOrBoundAfterClose(t *testing.T) {
	store, _ := installedFixture(t)
	controller, err := NewController(store, startFunc(func(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
		return &invocationSession{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	registry := &ManagedServices{}
	if err = controller.AttachManagedServices(nil); err == nil {
		t.Fatal("nil registry bound")
	}
	if err = controller.AttachManagedServices(registry); err != nil {
		t.Fatal(err)
	}
	if err = controller.AttachManagedServices(registry); err != nil {
		t.Fatal("same registry could not be attached idempotently", err)
	}
	if err = controller.AttachManagedServices(&ManagedServices{}); err == nil {
		t.Fatal("live registry swapped")
	}
	if err = controller.Close(); err != nil {
		t.Fatal(err)
	}
	if err = controller.AttachManagedServices(registry); err == nil {
		t.Fatal("closed controller accepted existing registry")
	}
	if err = controller.AttachManagedServices(&ManagedServices{}); err == nil {
		t.Fatal("closed controller accepted new registry")
	}
}
