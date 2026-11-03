package admin

import (
	"context"
	"encoding/json"
	"testing"
)

func TestIdleSettingsPersistWithoutInterruptingWorkerOrChangingLegacySettings(t *testing.T) {
	f, _ := trustedAgent(t)
	f.login()
	worker, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.manager.mu.Lock()
	f.manager.cancel = cancel
	f.manager.mu.Unlock()
	before := f.manager.agent.config()
	if got := before.retention(); got != (serviceRetention{PluginMinutes: 10, MusicMinutes: 15}) {
		t.Fatalf("defaults: %+v", got)
	}
	next := before
	next.ServiceRetention = &serviceRetention{PluginMinutes: 0, MusicMinutes: 3}
	code, raw, _ := f.request("POST", "/api/agent/config", next, true, true)
	if code != 200 {
		t.Fatalf("save: %d %s", code, raw)
	}
	if worker.Err() != nil {
		t.Fatal("changing retention interrupted an active worker")
	}
	var saved agentSettings
	if err := json.Unmarshal(raw, &saved); err != nil || saved.retention() != next.retention() {
		t.Fatal("saved retention differs", err)
	}
	saved.ServiceRetention = nil
	if err := f.manager.agent.save(saved); err != nil {
		t.Fatal(err)
	}
	loaded, err := newAgentControl(f.manager.agent.dir)
	if err != nil || loaded.config().retention() != next.retention() {
		t.Fatal("legacy save or reopen reset retention", err)
	}
	invalid := loaded.config()
	invalid.ServiceRetention.MusicMinutes = -1
	if err := loaded.save(invalid); err == nil {
		t.Fatal("negative timeout accepted")
	}
}
