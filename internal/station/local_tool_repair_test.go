package station

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestProjectToolRepairPreconditions(t *testing.T) {
	s := &localProjectTools{}
	for _, tc := range []struct {
		view localProjectToolView
		want string
	}{
		{localProjectToolView{ID: "visual-generation"}, "needs_installation"},
		{localProjectToolView{ID: "speech", Installed: true}, "disabled"},
		{localProjectToolView{ID: "comfyui", Installed: true, Enabled: true, Reason: "comfyui_workflows_missing"}, "needs_configuration"},
		{localProjectToolView{ID: localTTSPackage, Installed: true, Enabled: true, RuntimeReady: true}, "ready"},
	} {
		state, err := s.ensureProjectToolReady(t.Context(), nil, tc.view, true)
		if err != nil || state != tc.want {
			t.Fatalf("%s: state=%s err=%v, want %s", tc.view.ID, state, err, tc.want)
		}
	}
	if got := projectToolRepairResult(localProjectToolView{}, "failed", errors.New("private path")); got["reason"] != "start_failed" || got["next_action"] == nil {
		t.Fatal("repair failure missing safe guidance", got)
	}
}

func TestStationLifecycleCatalogUsesManageEntry(t *testing.T) {
	tools := stationLifecycleTools()
	if len(tools) != 1 || tools[0].Name != "station_tool_manage" {
		t.Fatal(tools)
	}
}

func TestCompactProjectToolStatusKeepsDetailsOnDemand(t *testing.T) {
	views := []localProjectToolView{{ID: "rvc", Name: "RVC", Installed: true, Enabled: true, State: "idle", RuntimeReady: true, PackageDigest: strings.Repeat("x", 10000)}}
	encoded, err := json.Marshal(compactProjectToolStatus(views))
	if err != nil || len(encoded) >= 6000 || strings.Contains(string(encoded), "package_digest") || !strings.Contains(string(encoded), `"runtime_ready":true`) {
		t.Fatalf("status list is not compact: bytes=%d err=%v", len(encoded), err)
	}
}
