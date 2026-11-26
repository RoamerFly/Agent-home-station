package admin

import (
	"encoding/json"
	"testing"
)

func TestStationToolManageUsesActualWeChatPackageAndRetainsEnableIntent(t *testing.T) {
	f, p := trustedAgent(t)
	store, _ := installPublicWebFixture(t, f)
	call := func(action string) wechatToolView {
		t.Helper()
		args, _ := json.Marshal(map[string]string{"action": action, "tool_id": "web-search"})
		text, err := p.callStationTool(t.Context(), "manage", args)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		var view wechatToolView
		if err := json.Unmarshal([]byte(text), &view); err != nil {
			t.Fatal(err)
		}
		if view.ExecutionHost != "wechat" || view.ID != "web-search" || !view.Enabled {
			t.Fatalf("wrong execution host or enabled intent: %+v", view)
		}
		return view
	}
	if view := call("status"); view.State != "stopped" {
		t.Fatalf("initial state: %+v", view)
	}
	if view := call("ensure_ready"); view.State != "running" {
		t.Fatalf("not started: %+v", view)
	}
	if view := call("stop"); view.State != "stopped" {
		t.Fatalf("not stopped: %+v", view)
	}
	if view := call("repair"); view.State != "running" {
		t.Fatalf("not repaired: %+v", view)
	}
	installed, err := store.Installed(t.Context(), "web-search")
	if err != nil || !installed.Enabled {
		t.Fatalf("stop changed persisted enable intent: %v %+v", err, installed)
	}
}
