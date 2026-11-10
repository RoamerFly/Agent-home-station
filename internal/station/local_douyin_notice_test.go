package station

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"wechat-bot/internal/stationbrowser"
)

func TestLocalDouyinNoticeRequiresSeparateOwnerConsentAndSingleUseRead(t *testing.T) {
	f, manager := newLocalDouyinWebFixture(t)
	initializeDouyinWeb(t, f)
	_, login := f.request("POST", localDouyinWebRoute, map[string]any{"action": "begin", "revision": 0, "approve_profile": true}, true)
	_, login = f.request("POST", localDouyinWebRoute, map[string]any{"action": "confirm", "revision": login["revision"], "attempt_id": login["attempt_id"]}, true)
	account := strings.Repeat("a", 64)
	probes, reads := 0, 0
	revokeDuringRead := false
	manager.probeNotice = func(_ context.Context, _ string, check func() error) (string, error) {
		probes++
		return account, check()
	}
	manager.readNotice = func(_ context.Context, _ string, request stationbrowser.DouyinNoticeRequest, check func() error) (stationbrowser.DouyinNoticePage, error) {
		if request.Account != account || !request.AllowMarkRead {
			t.Error("read lost its account or explicit marking authorization")
		}
		reads++
		if err := check(); err != nil {
			return stationbrowser.DouyinNoticePage{}, err
		}
		if revokeDuringRead {
			code, _ := f.request("POST", localDouyinNoticePath, map[string]any{"action": "revoke", "revision": login["revision"]}, true)
			if code != http.StatusOK {
				t.Errorf("in-flight revocation status = %d", code)
			}
		}
		return stationbrowser.DouyinNoticePage{Account: account, HasMore: true, Items: []stationbrowser.DouyinNotice{
			{ID: "1234", Type: 2, CreatedAt: 1700000000, WorkID: "5678"},
		}}, nil
	}
	post := func(action string, extra map[string]any, csrf bool) (int, map[string]any) {
		payload := map[string]any{"action": action, "revision": login["revision"]}
		for key, value := range extra {
			payload[key] = value
		}
		return f.request("POST", localDouyinNoticePath, payload, csrf)
	}
	_, view := f.request("GET", localDouyinWebRoute, nil, false)
	if view["notice_bound"] != false || probes != 0 || reads != 0 {
		t.Fatalf("opening status touched the platform: %v", view)
	}
	if status, _ := post("bind", nil, false); status != http.StatusForbidden || probes != 0 {
		t.Fatalf("binding bypassed CSRF: %d", status)
	}
	if status, _ := post("prepare", map[string]any{"allow_mark_read": true}, true); status != http.StatusForbidden {
		t.Fatalf("unbound prepare: %d", status)
	}
	status, bound := post("bind", nil, true)
	if status != http.StatusOK || bound["notice_bound"] != true || bound["notice_account"] != nil || probes != 1 || reads != 0 {
		t.Fatalf("bind = %d %v, probes=%d reads=%d", status, bound, probes, reads)
	}
	login["revision"] = bound["revision"]
	if status, _ := post("prepare", nil, true); status != http.StatusForbidden {
		t.Fatalf("prepare without marking consent: %d", status)
	}
	prepare := func() map[string]any {
		t.Helper()
		status, prepared := post("prepare", map[string]any{"allow_mark_read": true}, true)
		if status != http.StatusOK || prepared["ticket"] == nil {
			t.Fatalf("prepare = %d %v", status, prepared)
		}
		return map[string]any{"allow_mark_read": true, "ticket": prepared["ticket"]}
	}
	payload := prepare()
	status, page := post("read", payload, true)
	rows, ok := page["items"].([]any)
	if status != http.StatusOK || !ok || len(rows) != 1 || page["has_more"] != true || page["account"] != nil || reads != 1 {
		t.Fatalf("read = %d %v, reads=%d", status, page, reads)
	}
	if status, _ := post("read", payload, true); status != http.StatusConflict || reads != 1 {
		t.Fatalf("duplicate ticket reached browser: %d, reads=%d", status, reads)
	}
	revokeDuringRead = true
	payload = prepare()
	if status, page := post("read", payload, true); status != http.StatusConflict || page["items"] != nil || reads != 2 {
		t.Fatalf("late result passed revocation: %d %v, reads=%d", status, page, reads)
	}
	_, view = f.request("GET", localDouyinWebRoute, nil, false)
	if view["notice_bound"] != false {
		t.Fatalf("revocation not persisted: %v", view)
	}
}
