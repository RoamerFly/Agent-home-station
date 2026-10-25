package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"wechat-bot/internal/companion"
)

func TestProactiveConfigurationRoundtripAndAllowedNamedTargets(t *testing.T) {
	f := setup(t)
	if code, _, res := f.request("GET", "/assets/proactive.js", nil, false, false); code != 200 || res.Header.Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatal("proactive module not served", code, res.Header.Get("Content-Type"))
	}
	if code, _, _ := f.request("GET", "/api/companion/targets", nil, false, false); code != 401 {
		t.Fatal(code)
	}
	f.login()
	f.manager.native = &namedNative{f.native}
	c := f.manager.Config().Config
	c.PrivateEnabled = true
	c.DirectIDs = []string{"friend"}
	c.GroupRules = []GroupRule{{ID: "room@chatroom", Name: "配置的群名", Mode: "mention"}}
	c.Companion.ProactiveThreads = []string{"room@chatroom", "friend"}
	c.Companion.ProactiveRules = []companion.ProactiveRule{{Thread: "friend", IdleMinutes: 120, MaxPerDay: 2}}
	code, body, _ := f.request("POST", "/api/config", ConfigInput{Config: c}, true, true)
	if code != 200 {
		t.Fatal(code, string(body))
	}
	var view ConfigView
	_ = json.Unmarshal(body, &view)
	if len(view.Companion.ProactiveRules) != 1 || view.Companion.ProactiveRules[0].IdleMinutes != 120 || len(view.Companion.ProactiveThreads) != 2 {
		t.Fatal("roundtrip changed selection or overrides", string(body))
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved Config
	if json.Unmarshal(data, &saved) != nil || saved.Companion.ProactiveRules[0].MaxPerDay != 2 || len(saved.Companion.ProactiveThreads) != 2 {
		t.Fatal("scheduling config was not persisted")
	}
	// Returned snapshots cannot mutate the manager's scheduling data.
	view = f.manager.Config()
	view.Companion.ProactiveRules[0].IdleMinutes = 5
	if f.manager.Config().Companion.ProactiveRules[0].IdleMinutes != 120 {
		t.Fatal("shallow config copy")
	}
	code, body, _ = f.request("GET", "/api/companion/targets", nil, false, false)
	var targets struct {
		Items []proactiveTarget `json:"items"`
	}
	_ = json.Unmarshal(body, &targets)
	if code != 200 || len(targets.Items) != 3 {
		t.Fatal(code, string(body))
	}
	seen := map[string]bool{}
	for _, item := range targets.Items {
		seen[item.Kind] = true
		if item.ID != "filehelper" && item.Name != "会话备注" {
			t.Fatal("native name not resolved", item)
		}
	}
	if !seen["friend"] || !seen["group"] || !seen["filehelper"] {
		t.Fatal(seen)
	}
	// Turning off private replies invalidates the selected private target.
	c.PrivateEnabled = false
	if code, _, _ := f.request("POST", "/api/config", ConfigInput{Config: c}, true, true); code != 400 {
		t.Fatal("unauthorized proactive target saved", code)
	}
	c.PrivateEnabled = true
	c.Companion.ProactiveRules[0].Thread = "unallowed"
	if code, _, _ := f.request("POST", "/api/config", ConfigInput{Config: c}, true, true); code != 400 {
		t.Fatal("unselected override saved", code)
	}
}
