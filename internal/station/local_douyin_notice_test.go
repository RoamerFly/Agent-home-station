package station

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/stationbrowser"
)

func TestLocalDouyinNoticeRequiresSeparateOwnerConsentAndSingleUseRead(t *testing.T) {
	f, manager := newLocalDouyinWebFixture(t)
	initializeDouyinWeb(t, f)
	_, login := f.request("POST", localDouyinWebRoute, map[string]any{"action": "begin", "revision": 0, "approve_profile": true}, true)
	_, login = f.request("POST", localDouyinWebRoute, map[string]any{"action": "confirm", "revision": login["revision"], "attempt_id": login["attempt_id"]}, true)
	account := strings.Repeat("a", 64)
	checkpoint := stationbrowser.DouyinNoticeCheckpoint{Pages: 1, Digest: [32]byte{1}}
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
		if request.Pages < 1 || request.Pages > 5 {
			t.Error("lost effective page count")
		}
		if request.After.Pages != 0 && request.After != checkpoint {
			t.Error("lost trusted checkpoint")
		}
		if err := check(); err != nil {
			return stationbrowser.DouyinNoticePage{}, err
		}
		if revokeDuringRead {
			code, _ := f.request("POST", localDouyinNoticePath, map[string]any{"action": "revoke", "revision": login["revision"]}, true)
			if code != http.StatusOK {
				t.Errorf("in-flight revocation status = %d", code)
			}
		}
		return stationbrowser.DouyinNoticePage{Account: account, HasMore: request.After.Pages == 0, PagesRead: 1, Next: checkpoint, RenderedText: "合成通知正文", Items: []stationbrowser.DouyinNotice{
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
	prepare := func(extra ...map[string]any) map[string]any {
		t.Helper()
		parameters := map[string]any{"allow_mark_read": true}
		if len(extra) > 0 {
			for key, value := range extra[0] {
				parameters[key] = value
			}
		}
		status, prepared := post("prepare", parameters, true)
		if status != http.StatusOK || prepared["ticket"] == nil {
			t.Fatalf("prepare = %d %v", status, prepared)
		}
		parameters["ticket"] = prepared["ticket"]
		return parameters
	}
	payload := prepare()
	if status, _ := post("read", map[string]any{"ticket": payload["ticket"], "allow_mark_read": true, "pages": 2}, true); status != http.StatusConflict || reads != 0 {
		t.Fatalf("changed batch count reached browser: %d", status)
	}
	status, page := post("read", payload, true)
	rows, ok := page["items"].([]any)
	if status != http.StatusOK || !ok || len(rows) != 1 || page["has_more"] != true || page["rendered_text"] != "合成通知正文" || page["account"] != nil || page["pages_read"] != float64(1) || page["effective_pages"] != float64(1) || page["continuation"] == "" || reads != 1 {
		t.Fatalf("read = %d %v, reads=%d", status, page, reads)
	}
	if status, _ := post("read", payload, true); status != http.StatusConflict || reads != 1 {
		t.Fatalf("duplicate ticket reached browser: %d, reads=%d", status, reads)
	}
	cursor := page["continuation"]
	other := *f
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	other.client = &http.Client{Jar: jar}
	other.login()
	if status, _ := other.request("POST", localDouyinNoticePath, map[string]any{"action": "prepare", "revision": login["revision"], "allow_mark_read": true, "continuation": cursor}, true); status != http.StatusConflict || reads != 1 {
		t.Fatalf("continuation crossed owner browser sessions: %d", status)
	}
	if status, _ := post("prepare", map[string]any{"allow_mark_read": true, "continuation": "forged"}, true); status != http.StatusConflict {
		t.Fatalf("forged cursor: %d", status)
	}
	payload = prepare(map[string]any{"pages": 2, "continuation": cursor})
	if status, _ := post("read", map[string]any{"ticket": payload["ticket"], "allow_mark_read": true, "pages": 2}, true); status != http.StatusConflict || reads != 1 {
		t.Fatalf("changed continuation reached browser: %d", status)
	}
	status, next := post("read", payload, true)
	if status != http.StatusOK || next["has_more"] != false || next["continuation"] != "" || next["effective_pages"] != float64(2) || reads != 2 {
		t.Fatalf("continuation read: %d %v", status, next)
	}
	if status, _ := post("prepare", map[string]any{"allow_mark_read": true, "continuation": cursor}, true); status != http.StatusConflict {
		t.Fatalf("consumed continuation reused: %d", status)
	}
	manager.mu.Lock()
	manager.noticeCursors["expired"] = localDouyinNoticeContinuation{expires: time.Now().Add(-time.Second)}
	manager.mu.Unlock()
	if status, _ := post("prepare", map[string]any{"allow_mark_read": true, "continuation": "expired"}, true); status != http.StatusConflict {
		t.Fatalf("expired continuation reused: %d", status)
	}
	for _, count := range []int{-1, 6} {
		if status, _ := post("prepare", map[string]any{"allow_mark_read": true, "pages": count}, true); status != http.StatusBadRequest {
			t.Fatalf("explicit batch silently clamped: %d", status)
		}
	}
	revokeDuringRead = true
	payload = prepare()
	if status, page := post("read", payload, true); status != http.StatusConflict || page["items"] != nil || page["rendered_text"] != nil || reads != 3 {
		t.Fatalf("late result passed revocation: %d %v, reads=%d", status, page, reads)
	}
	_, view = f.request("GET", localDouyinWebRoute, nil, false)
	if view["notice_bound"] != false {
		t.Fatalf("revocation not persisted: %v", view)
	}
}
