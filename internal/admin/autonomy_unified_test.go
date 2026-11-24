package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wechat-bot/internal/companion"
)

func TestLegacyAutonomyMigrationKeepsTargetsButStartsDisabled(t *testing.T) {
	f := setup(t)
	if err := f.manager.Close(); err != nil {
		t.Fatal(err)
	}
	c := defaults()
	c.Companion.Permissions.Proactive = true
	c.Companion.ProactiveThreads = []string{"filehelper"}
	configData, _ := json.Marshal(c)
	if err := writePrivate(filepath.Join(f.dir, "config.json"), configData); err != nil {
		t.Fatal(err)
	}
	surf := surfDefaults()
	surf.Settings.Enabled = false
	surfData, _ := json.Marshal(surf)
	if err := writePrivate(filepath.Join(f.dir, "surf.json"), surfData); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewManager(f.dir, "", f.native)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.surf.Settings.Enabled || reopened.config.Companion.Permissions.Proactive || len(reopened.surf.Settings.ProactiveThreads) != 1 || reopened.surf.Settings.ProactiveThreads[0] != "filehelper" {
		t.Fatal("legacy mismatch enabled autonomous behavior or lost selected target")
	}
	if current, err := os.ReadFile(filepath.Join(f.dir, "surf.json")); err != nil || string(current) != string(surfData) {
		t.Fatal("reading legacy settings unexpectedly rewrote stored state", err)
	}
}

func TestUnifiedSurfSaveOwnsSwitchAndSurvivesConfigSaveAndRestart(t *testing.T) {
	f := setup(t)
	f.login()
	settings := surfDefaults().Settings
	settings.Enabled = true
	settings.ProactiveThreads = []string{"filehelper"}
	settings.ProactiveRules = []companion.ProactiveRule{{Thread: "filehelper", IdleMinutes: 90, MaxPerDay: 1}}
	if code, body, _ := f.request("POST", "/api/surf", settings, true, true); code != 200 {
		t.Fatal(code, string(body))
	}
	if !f.manager.surf.AutonomyUnified || !f.manager.surf.Settings.Enabled || !f.manager.config.Companion.Permissions.Proactive || len(f.manager.config.Companion.ProactiveThreads) != 1 {
		t.Fatal("single save did not update the active autonomous configuration")
	}
	legacyRequest := settings
	legacyRequest.ProactiveThreads, legacyRequest.ProactiveRules = nil, nil
	if code, body, _ := f.request("POST", "/api/surf", legacyRequest, true, true); code != 200 || len(f.manager.config.Companion.ProactiveThreads) != 1 {
		t.Fatal("older surf request erased conversation targets", code, string(body))
	}
	stale := f.manager.Config().Config
	stale.Companion.Permissions.Proactive = false
	stale.Companion.ProactiveThreads = nil
	stale.Companion.ProactiveRules = nil
	if _, err := f.manager.Save(ConfigInput{Config: stale}); err != nil {
		t.Fatal(err)
	}
	if !f.manager.config.Companion.Permissions.Proactive || len(f.manager.config.Companion.ProactiveThreads) != 1 {
		t.Fatal("stale config form replaced the unified setting")
	}
	if err := f.manager.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewManager(f.dir, "", f.native)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reopened.surf.Settings.Enabled || !reopened.config.Companion.Permissions.Proactive || len(reopened.config.Companion.ProactiveThreads) != 1 || reopened.config.Companion.ProactiveRules[0].IdleMinutes != 90 {
		t.Fatal("unified autonomous settings did not survive restart")
	}
}

func TestTimedOutSurfStopResumesPreviousWorkerWhenItFinishes(t *testing.T) {
	f := setup(t)
	oldDone := make(chan struct{})
	f.manager.mu.Lock()
	f.manager.config.Enabled = true
	f.manager.done = oldDone
	f.manager.cancel = func() {}
	f.manager.mu.Unlock()
	finished := make(chan struct{})
	go func() {
		f.manager.resumeAfterSurfStop(oldDone)
		close(finished)
	}()
	close(oldDone)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("timed-out surf stop did not recover after the old worker exited")
	}
	f.manager.mu.Lock()
	restarted := f.manager.done != nil && f.manager.done != oldDone && f.manager.status.State == "waiting_login"
	f.manager.mu.Unlock()
	if !restarted {
		t.Fatal("previous worker was not restarted")
	}
}
