package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wechat-bot/internal/mcptools"
)

func TestSurfMissingDisabledOrUnreadyPluginNeverConsumesSchedule(t *testing.T) {
	for _, state := range []string{"missing", "disabled", "runtime_unavailable"} {
		t.Run(state, func(t *testing.T) {
			f := setup(t)
			if state != "missing" {
				s, i := installPublicWebFixture(t, f)
				if state == "disabled" {
					if _, err := s.SetEnabled(t.Context(), i.Package.ID, i.Revision, false); err != nil {
						t.Fatal(err)
					}
				} else {
					f.manager.packageRuntimeReady = false
				}
			}
			f.manager.config.Companion.Enabled = true
			f.manager.config.Companion.Permissions.Proactive = true
			f.manager.config.Companion.QuietStart, f.manager.config.Companion.QuietEnd = "00:00", "00:00"
			f.manager.surf.Settings.Enabled = true
			f.manager.surf.Settings.Threads = []string{"filehelper"}
			before, _ := json.Marshal(f.manager.surf)
			task, err := f.manager.claimSurf(time.Now())
			after, _ := json.Marshal(f.manager.surf)
			var web *mcptools.WebError
			if task != nil || !errors.As(err, &web) || !bytes.Equal(before, after) {
				t.Fatal("unavailable plugin consumed browsing budget", state, task, err)
			}
			if _, err := os.Stat(filepath.Join(f.dir, "surf.json")); !os.IsNotExist(err) {
				t.Fatal("failed preflight wrote schedule", state, err)
			}
		})
	}
}

func TestSurfQuietHoursSkipWithoutConsumingOrMutatingSchedule(t *testing.T) {
	f := setup(t)
	installPublicWebFixture(t, f)
	c := &f.manager.config.Companion
	c.Enabled = true
	c.Permissions.Proactive = true
	c.QuietStart = "23:00"
	c.QuietEnd = "08:00"
	c.Timezone = "Asia/Shanghai"
	f.manager.surf = surfDefaults()
	f.manager.surf.Settings.Enabled = true
	f.manager.surf.Settings.Threads = []string{"filehelper"}
	before, _ := json.Marshal(f.manager.surf)
	path := filepath.Join(f.dir, "surf.json")
	if err := writePrivate(path, before); err != nil {
		t.Fatal(err)
	}
	for _, stamp := range []string{"2026-10-25T23:00:00+08:00", "2026-10-26T00:00:00+08:00", "2026-10-26T07:59:59+08:00"} {
		now, _ := time.Parse(time.RFC3339, stamp)
		if task, err := f.manager.claimSurf(now); err != nil || task != nil {
			t.Fatal("quiet task claimed", task, err)
		}
		after, _ := os.ReadFile(path)
		current, _ := json.Marshal(f.manager.surf)
		if !bytes.Equal(before, after) || !bytes.Equal(before, current) {
			t.Fatal("quiet skip mutated budget/day/topic/next")
		}
		if f.manager.surfAllowedAt("filehelper", now) {
			t.Fatal("sharing allowed during quiet")
		}
	}
	now, _ := time.Parse(time.RFC3339, "2026-10-26T08:00:00+08:00")
	task, err := f.manager.claimSurf(now)
	if err != nil || task == nil || task.Thread != "filehelper" || f.manager.surf.Sessions != 1 || f.manager.surf.TopicIndex != 1 {
		t.Fatal("wake boundary not exactly one new task", task, err)
	}
	want, _ := time.Parse(time.RFC3339, "2026-10-26T23:00:00+08:00")
	if !task.QuietDeadline.Equal(want) {
		t.Fatal("task not bounded to quiet start", task.QuietDeadline)
	}
	loaded, err := loadSurf(f.dir)
	if err != nil || loaded.Sessions != 1 {
		t.Fatal("claim not persisted", err)
	}
	if task, err = f.manager.claimSurf(now.Add(time.Second)); err != nil || task != nil {
		t.Fatal("missed quiet periods caught up as extra jobs")
	}
}

func TestSurfTargetSelectionRoundtripAndDailyDisplay(t *testing.T) {
	f := setup(t)
	f.login()
	f.manager.config.PrivateEnabled = true
	f.manager.config.DirectIDs = []string{"friend"}
	f.manager.config.GroupRules = []GroupRule{{ID: "room@chatroom", Mode: "mention"}}
	s := surfDefaults().Settings
	s.ForceShare = true
	s.Threads = []string{"room@chatroom", "friend"}
	if code, _, _ := f.request("POST", "/api/surf", s, true, true); code != 200 {
		t.Fatal(code)
	}
	loaded, err := loadSurf(f.dir)
	if err != nil || !loaded.Settings.ForceShare || len(loaded.Settings.Threads) != 2 || loaded.Settings.Threads[0] != "room@chatroom" || loaded.Settings.Threads[1] != "friend" {
		t.Fatal("selection not persisted", err)
	}
	for _, ids := range [][]string{{"friend", "friend"}, {"unapproved"}} {
		s.Threads = ids
		if code, _, _ := f.request("POST", "/api/surf", s, true, true); code != 400 {
			t.Fatal("invalid targets accepted", code)
		}
	}
	f.manager.surf.Day = "2020-01-01"
	f.manager.surf.Sessions = 4
	code, body, _ := f.request("GET", "/api/surf", nil, false, false)
	var state struct {
		Sessions   int    `json:"sessions"`
		QuietStart string `json:"quiet_start"`
		Timezone   string `json:"timezone"`
	}
	_ = json.Unmarshal(body, &state)
	if code != 200 || state.Sessions != 0 || state.QuietStart != "23:00" || state.Timezone != "Asia/Shanghai" || f.manager.surf.Sessions != 4 {
		t.Fatal("display count is stale or GET mutated ledger", string(body))
	}
}

func TestSurfAccessBudgetAndExplicitTargets(t *testing.T) {
	f := setup(t)
	if code, _, _ := f.request("GET", "/api/surf", nil, false, false); code != 401 {
		t.Fatal("surf logs unprotected")
	}
	f.login()
	s := surfDefaults().Settings
	s.Enabled = true
	s.MaxSessionsPerDay = 1
	s.Threads = []string{"unapproved"}
	if code, _, _ := f.request("POST", "/api/surf", s, true, true); code != 400 {
		t.Fatal("unapproved share target saved")
	}
	s.Threads = nil
	if code, _, _ := f.request("POST", "/api/surf", s, true, false); code != 403 {
		t.Fatal("surf save omitted CSRF")
	}
	if code, _, _ := f.request("POST", "/api/surf", s, true, true); code != 200 {
		t.Fatal("save failed", code)
	}
	f.manager.mu.Lock()
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.QuietStart = "00:00"
	f.manager.config.Companion.QuietEnd = "00:00"
	f.manager.surf.Next = time.Time{}
	f.manager.mu.Unlock()
	task, err := f.manager.claimSurf(time.Now())
	if err == nil || task != nil || f.manager.surf.Sessions != 0 {
		t.Fatal("unmigrated browse-only consumed budget", task, err)
	}
	installPublicWebFixture(t, f)
	if task, err = f.manager.claimSurf(time.Now()); err != nil || task == nil || task.Thread != "" || task.PrivateBrowseTools == nil || f.manager.surf.Sessions != 1 {
		t.Fatal("installed private browsing failed to claim its own readonly origin", task, err)
	}
	f.manager.mu.Lock()
	f.manager.surf.Next = time.Time{}
	f.manager.mu.Unlock()
	if task, err = f.manager.claimSurf(time.Now()); err != nil || task != nil {
		t.Fatal("daily budget not enforced")
	}
	loaded, err := loadSurf(f.dir)
	if err != nil || loaded.Sessions != 1 {
		t.Fatal("budget not persisted", err)
	}
}

func TestFeedDiscoveryCanRunWithSearchDisabledButNotWebReadDisabled(t *testing.T) {
	f := setup(t)
	installPublicWebFixture(t, f)
	f.manager.surf = surfDefaults()
	f.manager.surf.Settings.Enabled = true
	f.manager.surf.Settings.Threads = []string{"filehelper"}
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.Proactive = true
	f.manager.config.Companion.QuietStart = "00:00"
	f.manager.config.Companion.QuietEnd = "00:00"
	f.manager.mcp.WebSearchEnabled = false
	if task, err := f.manager.claimSurf(time.Now()); err != nil || task == nil {
		t.Fatal("feed browsing unnecessarily required search", task, err)
	}
	f.manager.surf.Next = time.Time{}
	before := f.manager.surf.Sessions
	f.manager.mcp.WebFetchEnabled = false
	if task, err := f.manager.claimSurf(time.Now()); err != nil || task != nil || f.manager.surf.Sessions != before {
		t.Fatal("disabled read permission consumed session", task, err)
	}
}
