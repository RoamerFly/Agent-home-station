package admin

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestFishLocalIdentityInitializesWithoutApplicationAndPersists(t *testing.T) {
	dir := t.TempDir()
	registry := newFishIdentityRegistry(dir)
	before, err := registry.localIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if before.FishID != "" || before.LocalOwner != nil {
		t.Fatalf("a read of an unconfigured registry must not initialize it: %#v", before)
	}
	if _, err := os.Stat(filepath.Join(dir, "fish-identity.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read unexpectedly wrote identity registry: %v", err)
	}

	created, err := registry.initializeLocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if !validFishID(created.FishID) || created.LocalOwner == nil || !validFishLocalOwner(*created.LocalOwner) || !created.LocalOwner.HomeAccess || created.LocalOwner.Revision != 1 {
		t.Fatalf("unexpected initialized identity: %#v", created)
	}
	if len(created.Bindings) != 0 {
		t.Fatalf("local initialization unexpectedly bound an application: %#v", created.Bindings)
	}

	afterRestart, err := newFishIdentityRegistry(dir).localIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if afterRestart.FishID != created.FishID || afterRestart.LocalOwner == nil || *afterRestart.LocalOwner != *created.LocalOwner {
		t.Fatalf("local identity did not survive restart: got %#v, want %#v", afterRestart, created)
	}
}

func TestFishLocalIdentityPauseIsNotReenabledAndUsesCAS(t *testing.T) {
	registry := newFishIdentityRegistry(t.TempDir())
	created, err := registry.initializeLocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	paused, err := registry.setLocalHomeAccess(created.LocalOwner.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	if paused.LocalOwner.HomeAccess || paused.LocalOwner.Revision != 2 {
		t.Fatalf("pause not recorded: %#v", paused.LocalOwner)
	}
	retried, err := registry.initializeLocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if retried.LocalOwner.HomeAccess || retried.LocalOwner.Revision != paused.LocalOwner.Revision {
		t.Fatalf("initialize silently reenabled paused grant: %#v", retried.LocalOwner)
	}
	if _, err := registry.setLocalHomeAccess(1, true); !errors.Is(err, errLocalHomeRevisionConflict) {
		t.Fatalf("stale revision error = %v", err)
	}
	resumed, err := registry.setLocalHomeAccess(2, true)
	if err != nil || !resumed.LocalOwner.HomeAccess || resumed.LocalOwner.Revision != 3 {
		t.Fatalf("resume = %#v, %v", resumed.LocalOwner, err)
	}
	if _, err := newFishIdentityRegistry(t.TempDir()).setLocalHomeAccess(1, true); !errors.Is(err, errLocalHomeNotInitialized) {
		t.Fatalf("uninitialized mutation error = %v", err)
	}
}

func TestFishLocalIdentitySurvivesWechatUnbind(t *testing.T) {
	registry := newFishIdentityRegistry(t.TempDir())
	created, err := registry.initializeLocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.bind("local-owner-wechat"); err != nil {
		t.Fatal(err)
	}
	if err := registry.unbind("local-owner-wechat"); err != nil {
		t.Fatal(err)
	}
	got, err := registry.localIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if got.FishID != created.FishID || got.LocalOwner == nil || *got.LocalOwner != *created.LocalOwner || len(got.Bindings) != 0 {
		t.Fatalf("WeChat unbind changed local owner: %#v", got)
	}
}

func TestFishLocalIdentityReadsLegacyWithoutAutoAuthorization(t *testing.T) {
	for _, version := range []int{fishIdentityVersionV1, fishIdentityVersionV2} {
		t.Run("v"+string(rune('0'+version)), func(t *testing.T) {
			dir := t.TempDir()
			fixture := `{"version":` + string(rune('0'+version)) + `,"fish_id":"fish-0123456789abcdef0123456789abcdef0123456789abcdef","bindings":[]}`
			if err := os.WriteFile(filepath.Join(dir, "fish-identity.json"), []byte(fixture), 0600); err != nil {
				t.Fatal(err)
			}
			registry := newFishIdentityRegistry(dir)
			read, err := registry.localIdentity()
			if err != nil {
				t.Fatal(err)
			}
			if read.Version != version || read.LocalOwner != nil {
				t.Fatalf("legacy read auto-migrated or authorized: %#v", read)
			}
			created, err := registry.initializeLocalIdentity()
			if err != nil {
				t.Fatal(err)
			}
			if created.Version != fishIdentityCurrentVersion || created.LocalOwner == nil || !created.LocalOwner.HomeAccess {
				t.Fatalf("explicit legacy initialization failed: %#v", created)
			}
		})
	}
}

func TestFishLocalIdentityRejectsDamagedOwnerWithoutOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fish-identity.json")
	damaged := []byte(`{"version":3,"fish_id":"fish-0123456789abcdef0123456789abcdef0123456789abcdef","bindings":[],"local_owner":{"id":"owner-bad","revision":0,"home_access":true}}`)
	if err := os.WriteFile(path, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	registry := newFishIdentityRegistry(dir)
	if _, err := registry.initializeLocalIdentity(); err == nil {
		t.Fatal("damaged registry initialized")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(damaged) {
		t.Fatalf("damaged registry overwritten: %s", got)
	}
}

func TestFishLocalIdentityAndWechatBindingDoNotLoseConcurrentUpdates(t *testing.T) {
	dir := t.TempDir()
	local := newFishIdentityRegistry(dir)
	wechat := newFishIdentityRegistry(dir) // simulates the legacy manager process.
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, err := local.initializeLocalIdentity()
		errs <- err
	}()
	go func() {
		defer wg.Done()
		<-start
		_, err := wechat.bind("concurrent-wechat")
		errs <- err
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := newFishIdentityRegistry(dir).localIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if got.LocalOwner == nil || !got.LocalOwner.HomeAccess || !fishAccountIsBound(got, "concurrent-wechat") {
		t.Fatalf("concurrent update was lost: %#v", got)
	}
}
