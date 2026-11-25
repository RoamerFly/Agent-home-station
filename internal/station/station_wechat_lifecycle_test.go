package station

import (
	"errors"
	"testing"
	"time"

	"wechat-bot/internal/apphost"
)

func TestLegacyDesktopModeRejectsManualStationWorkerStart(t *testing.T) {
	t.Setenv("AHS_LEGACY_WECHAT", "1")
	host := &stationWechat{}
	if _, err := host.SetConnectionRunning(t.Context(), "wechat-synthetic", true); !errors.Is(err, apphost.ErrLifecycleCutover) {
		t.Fatalf("legacy-owned account allowed a second worker: %v", err)
	}
	if _, err := host.SetConnectionRunning(t.Context(), "wechat-synthetic", false); errors.Is(err, apphost.ErrLifecycleCutover) {
		t.Fatalf("legacy mode blocked a stop request: %v", err)
	}
}

func waitAppLifecycle(t *testing.T, ready func() bool) {
	t.Helper()
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ready() {
			return
		}
	}
	t.Fatal("expected connection lifecycle state not reached")
}
