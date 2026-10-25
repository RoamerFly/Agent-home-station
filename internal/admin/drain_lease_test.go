package admin

import (
	"encoding/json"
	"testing"
	"time"
)

func TestExpiredUpdateLeaseReleasesWithoutResendingOrChangingConfiguration(t *testing.T) {
	f := setup(t)
	f.login()
	before, _ := json.Marshal(f.manager.Config())
	_, b, _ := f.request("POST", "/api/runtime/drain", map[string]any{}, true, true)
	var out struct{ Ticket string }
	json.Unmarshal(b, &out)
	f.manager.mu.Lock()
	deadline := f.manager.drainExpires
	f.manager.mu.Unlock()
	if deadline.Before(time.Now().Add(20 * time.Second)) {
		t.Fatal("no bounded lease")
	}
	f.manager.expireDrain("wrong-ticket", deadline.Add(time.Second))
	if !f.manager.draining() {
		t.Fatal("different updater cleared lease")
	}
	f.manager.expireDrain(out.Ticket, deadline.Add(time.Second))
	if f.manager.draining() {
		t.Fatal("stale updater left worker paused")
	}
	after, _ := json.Marshal(f.manager.Config())
	if string(before) != string(after) {
		t.Fatal("lease changed configuration")
	}
}

func TestLeaseHeartbeatPreventsOldTimerFromReleasingActiveUpdater(t *testing.T) {
	f := setup(t)
	f.login()
	_, b, _ := f.request("POST", "/api/runtime/drain", map[string]any{}, true, true)
	var out struct{ Ticket string }
	json.Unmarshal(b, &out)
	f.manager.mu.Lock()
	old := time.Now().Add(-time.Second)
	f.manager.drainExpires = old
	f.manager.mu.Unlock()
	f.request("GET", "/api/runtime/drain?ticket="+out.Ticket, nil, true, true)
	f.manager.expireDrain(out.Ticket, time.Now())
	if !f.manager.draining() {
		t.Fatal("renewed lease expired")
	}
	f.request("POST", "/api/runtime/undrain", map[string]string{"ticket": out.Ticket}, true, true)
	if f.manager.draining() {
		t.Fatal("explicit release ignored")
	}
}
