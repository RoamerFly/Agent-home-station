package admin

import (
	"os"
	"path/filepath"
	"testing"

	"wechat-bot/internal/devicebroker"
	"wechat-bot/internal/pluginhost"
)

// Authority fixtures use a genuinely indexed, hash-verified package registration.
// They do not claim OS execution; the real LPAC stream test lives in devicebroker.
func registerSyntheticMobileNativeRuntime(t *testing.T, m *Manager, store *pluginhost.Store, i pluginhost.InstalledPackage) {
	t.Helper()
	code, err := store.PackageDirectory(i.Digest)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	python := filepath.Join(root, "python.exe")
	if err = os.WriteFile(python, []byte("synthetic authority fixture, never executed"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := devicebroker.NewRuntimeRegistration(t.Context(), code, pluginhost.Grant{Account: m.native.Account(), Plugin: i.Package.ID, Version: i.Package.Version, Permissions: i.Permissions}, devicebroker.RuntimeEnvironment{Python: python, RuntimeRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	m.pluginRuntimeBindings.Store("mobile-control:native", r)
}

func TestMobileNativeCapabilitiesRequireHostRegistration(t *testing.T) {
	f := setup(t)
	if err := f.manager.mobileDeviceReady(t.Context()); err == nil || err.Error() != "device_broker_runtime_registration_required" {
		t.Fatal("native device capabilities reported ready without native registration", err)
	}
	f.manager.pluginRuntimeBindings.Store("mobile-control:native", "untrusted settings cannot mint a registration")
	if err := f.manager.mobileDeviceReady(t.Context()); err == nil {
		t.Fatal("non-registration value unlocked native capabilities")
	}
}
