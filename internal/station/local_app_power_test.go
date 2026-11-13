package station

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"wechat-bot/internal/workspacefs"
)

func TestWechatAppPowerPausesOnlyEnabledConnections(t *testing.T) {
	enabledDir, inactiveDir := t.TempDir(), t.TempDir()
	enabled := &stationWechatWorker{dir: enabledDir, policy: stationWechatPolicy{Enabled: true}}
	// A connection the owner never enabled is outside the application switch.
	inactive := &stationWechatWorker{dir: inactiveDir, policy: stationWechatPolicy{Enabled: false, Paused: true}}
	s := &stationWechat{workers: map[string]*stationWechatWorker{"enabled": enabled, "inactive": inactive}}

	view := s.appPower()
	if !view.Enabled || view.Connections != 1 || view.Paused != 0 {
		t.Fatalf("initial app power view = %+v", view)
	}
	off, err := s.setAppPower(context.Background(), false)
	if err != nil || off.Enabled || off.Paused != 1 || off.Connections != 1 {
		t.Fatalf("power off view = %+v err=%v", off, err)
	}
	if policy := readWorkerPolicy(t, enabledDir); !policy.Paused || !policy.Enabled {
		t.Fatalf("enabled connection was not paused: %+v", policy)
	}
	// The inactive connection is outside the switch, so nothing was written.
	if _, err := os.Stat(filepath.Join(inactiveDir, "worker.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inactive connection was rewritten: %v", err)
	}
	on, err := s.setAppPower(context.Background(), true)
	if err != nil || !on.Enabled || on.Paused != 0 {
		t.Fatalf("power on view = %+v err=%v", on, err)
	}
	if policy := readWorkerPolicy(t, enabledDir); policy.Paused {
		t.Fatalf("connection stayed paused after power on: %+v", policy)
	}
}

func readWorkerPolicy(t *testing.T, dir string) stationWechatPolicy {
	t.Helper()
	root, err := workspacefs.OpenPath(dir)
	if err != nil {
		t.Fatalf("open worker directory: %v", err)
	}
	defer root.Close()
	policy, err := readStationWechatPolicy(root)
	if err != nil {
		t.Fatalf("read worker policy: %v", err)
	}
	return policy
}
