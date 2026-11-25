package station

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/pluginhost"
)

type repairLeaseRVC struct {
	fakeLocalRVCBackend
	busy    bool
	infoErr error
	calls   int
	stops   int
}

func (r *repairLeaseRVC) Info(context.Context, string) (localRVCDeployment, error) {
	if r.infoErr != nil {
		return localRVCDeployment{}, r.infoErr
	}
	return localRVCDeployment{Binding: "synthetic-binding", Profiles: []localRVCVoice{{ID: "synthetic-voice"}}, Running: true, Busy: r.busy}, nil
}

func (r *repairLeaseRVC) Call(context.Context, string, string, string, string, string, []byte) ([]byte, int, error) {
	r.calls++
	return nil, http.StatusServiceUnavailable, errors.New("synthetic health probe failed")
}

func (r *repairLeaseRVC) Stop(context.Context) error {
	r.stops++
	return nil
}

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

func TestProjectToolRepairWaitsForIndependentBackendLease(t *testing.T) {
	backend := &repairLeaseRVC{busy: true}
	s := &localProjectTools{rvc: backend}
	view := localProjectToolView{ID: localRVCPackage, Installed: true, Enabled: true, RuntimeReady: true, State: "running", Busy: true}
	if state, err := s.ensureProjectToolReady(t.Context(), nil, view, true); err != nil || state != "busy" || backend.calls != 0 || backend.stops != 0 {
		t.Fatalf("busy lease was touched: state=%s err=%v calls=%d stops=%d", state, err, backend.calls, backend.stops)
	}
	result := projectToolRepairResult(view, "busy", nil)
	if result["reason"] != "tool_has_active_work" || result["next_action"] != "wait_until_idle_then_repair" {
		t.Fatal("busy repair did not give wait guidance", result)
	}

	store, err := pluginhost.OpenStore(filepath.Join(t.TempDir(), "tools"), "synthetic-fish")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host := &localProjectToolHost{store: store}
	view.Busy = false // The view became stale before the health probe failed.
	if state, err := s.ensureProjectToolReady(t.Context(), host, view, true); err != nil || state != "busy" || backend.calls != 1 || backend.stops != 0 {
		t.Fatalf("late lease was not retained: state=%s err=%v calls=%d stops=%d", state, err, backend.calls, backend.stops)
	}
	backend.busy = false
	backend.infoErr = errors.New("synthetic lease state unreadable")
	if state, err := s.ensureProjectToolReady(t.Context(), host, view, true); err == nil || state != "failed" || backend.stops != 0 {
		t.Fatalf("unknown lease state allowed stop: state=%s err=%v stops=%d", state, err, backend.stops)
	}
	for _, reason := range []string{"managed_service_owner_busy", "tool_busy_wait_for_completion", "工具正在处理任务，请完成后再停止或重启"} {
		if !projectToolBusyError(errors.New(reason)) {
			t.Fatal("busy stop was treated as startup failure", reason)
		}
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
