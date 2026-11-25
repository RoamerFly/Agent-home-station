package admin

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCompactStationToolStatusFitsFeedback(t *testing.T) {
	views := make([]stationToolView, 15)
	for i := range views {
		views[i] = stationToolView{ID: fmt.Sprintf("tool-%02d", i), Name: "工具", Installed: true, Enabled: true, State: "running", RuntimeReady: true, VoiceProfiles: strings.Repeat("x", 10000)}
	}
	encoded, err := json.Marshal(map[string]any{"tools": compactStationToolStatus(views)})
	if err != nil || len(encoded) >= 5500 || strings.Contains(string(encoded), "voice_profiles") || !strings.Contains(string(encoded), `"runtime_ready":true`) {
		t.Fatalf("status list exceeds feedback budget: bytes=%d err=%v", len(encoded), err)
	}
}
