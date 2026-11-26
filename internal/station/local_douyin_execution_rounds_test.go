package station

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/stationbrowser"
)

func syntheticExecutionRoundFixture(t *testing.T, agent bool) (*localHomeFixture, *localDouyinWebLogin, map[string]any) {
	t.Helper()
	f, m := newLocalDouyinWebFixture(t)
	code, session := f.request("GET", "/api/session", nil, false)
	if code != http.StatusOK || session["authenticated"] != true {
		t.Fatalf("owner session: %d %v", code, session)
	}
	f.csrf, _ = session["csrf"].(string)
	if f.csrf == "" {
		t.Fatal("owner session has no CSRF token")
	}
	if code, out := f.request("POST", "/api/local/home", map[string]string{"action": "initialize"}, true); code != http.StatusOK {
		t.Fatalf("initialize: %d %v", code, out)
	}
	_, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "begin", "revision": 0, "approve_profile": true}, true)
	code, view = f.request("POST", localDouyinWebRoute, map[string]any{"action": "confirm", "revision": view["revision"], "attempt_id": view["attempt_id"]}, true)
	if code != http.StatusOK {
		t.Fatalf("confirm: %d %v", code, view)
	}
	m.probeNotice = func(_ context.Context, _ string, check func() error) (string, error) {
		return strings.Repeat("a", 64), check()
	}
	code, view = f.request("POST", localDouyinNoticePath, map[string]any{"action": "bind", "revision": view["revision"]}, true)
	if code != http.StatusOK {
		t.Fatalf("bind: %d %v", code, view)
	}
	if agent {
		view = setSyntheticNoticeAgent(t, f, true)
	}
	return f, m, view
}

func executionRoundDay() string { return time.Now().In(localDouyinExecutionZone).Format("2006-01-02") }

func grantExecutionNoticeMemory(t *testing.T, f *localHomeFixture, view map[string]any) map[string]any {
	t.Helper()
	code, next := f.request("POST", localDouyinWebRoute, map[string]any{
		"action": "set_memory", "revision": view["revision"], "memory_scope": douyinMemoryNotices, "allow_memory": true,
	}, true)
	if code != http.StatusOK {
		t.Fatalf("notice memory grant: %d %v", code, next)
	}
	return next
}

func TestLocalDouyinExecutionRoundsManualReadPaginationAndRevocation(t *testing.T) {
	f, m, view := syntheticExecutionRoundFixture(t, false)
	// A web notice saying "评论了你的作品" may refer to a child comment. Even
	// mixed list text cannot establish a per-notice target or parent author.
	const syntheticNoticeText = "有人评论了你的作品\n有人回复了你的评论"
	reads := 0
	m.readNotice = func(_ context.Context, _ string, in stationbrowser.DouyinNoticeRequest, check func() error) (stationbrowser.DouyinNoticePage, error) {
		reads++
		if err := check(); err != nil {
			return stationbrowser.DouyinNoticePage{}, err
		}
		return stationbrowser.DouyinNoticePage{Account: in.Account, PagesRead: 1, RenderedText: syntheticNoticeText, Items: []stationbrowser.DouyinNotice{
			{ID: "n-1", Type: 7, WorkID: "w-1", CreatedAt: 1700000000},
			{ID: "n-2", Type: 8, WorkID: "w-2"},
			{ID: "n-3", Type: 9},
		}}, nil
	}
	path := localDouyinExecutionRoundsPath + "?date=" + executionRoundDay()
	if code, out := f.request("GET", path, nil, false); code != http.StatusOK || len(out["rounds"].([]any)) != 0 || reads != 0 {
		t.Fatalf("opening execution rounds read platform: %d %v reads=%d", code, out, reads)
	}
	prepare := func() map[string]any {
		t.Helper()
		code, out := f.request("POST", localDouyinNoticePath, map[string]any{"action": "prepare", "revision": view["revision"], "allow_mark_read": true}, true)
		if code != http.StatusOK {
			t.Fatalf("prepare: %d %v", code, out)
		}
		return out
	}
	read := func(ticket any) (int, map[string]any) {
		return f.request("POST", localDouyinNoticePath, map[string]any{"action": "read", "revision": view["revision"], "allow_mark_read": true, "ticket": ticket}, true)
	}
	first := prepare()
	if code, out := read(first["ticket"]); code != http.StatusOK || len(out["items"].([]any)) != 3 {
		t.Fatalf("manual notice read: %d %v", code, out)
	}
	if code, out := read(first["ticket"]); code != http.StatusConflict || reads != 1 {
		t.Fatalf("ticket replay: %d %v reads=%d", code, out, reads)
	}
	if code, out := read(prepare()["ticket"]); code != http.StatusOK || reads != 2 {
		t.Fatalf("duplicate notice read: %d %v reads=%d", code, out, reads)
	}
	code, page := f.request("GET", path+"&limit=2", nil, false)
	rows, ok := page["rounds"].([]any)
	if code != http.StatusOK || !ok || len(rows) != 2 || page["next_before"] == "" || page["effective_limit"] != float64(2) || page["coverage"] != "notice_metadata" {
		t.Fatalf("first execution page: %d %v", code, page)
	}
	for _, raw := range rows {
		row := raw.(map[string]any)
		if row["state"] != "待核对" || row["reason"] != "证据不足" || row["reply_eligible"] != false || row["observed_at"] == "" {
			t.Fatalf("unverified notice became actionable: %v", row)
		}
	}
	if bytes.Contains([]byte(localAppObservationJSONString(page)), []byte(syntheticNoticeText)) {
		t.Fatal("notification body entered execution response")
	}
	code, next := f.request("GET", path+"&limit=2&before="+page["next_before"].(string), nil, false)
	if code != http.StatusOK || len(next["rounds"].([]any)) != 1 || next["next_before"] != "" {
		t.Fatalf("second execution page: %d %v", code, next)
	}
	identity, err := m.home.identity.Read()
	if err != nil || identity.LocalOwner == nil {
		t.Fatal(err)
	}
	record, _, err := m.noticeState(identity.FishID)
	if err != nil {
		t.Fatal(err)
	}
	foreign := record
	foreign.NoticeAccount = strings.Repeat("b", 64)
	for _, scope := range []string{
		localDouyinExecutionScope(record.FishID, "different-owner", record),
		localDouyinExecutionScope(record.FishID, identity.LocalOwner.ID, foreign),
	} {
		isolated, err := m.listExecutionRounds(t.Context(), scope, executionRoundDay(), 0, 20)
		if err != nil || len(isolated.Rounds) != 0 {
			t.Fatalf("owner or account crossed ledger scope: %v %+v", err, isolated)
		}
	}
	database := filepath.Join(stationApplicationsDir(f.dir), "douyin", "execution-rounds", "rounds.sqlite")
	raw, err := os.ReadFile(database)
	if err != nil || bytes.Contains(raw, []byte(syntheticNoticeText)) || bytes.Contains(raw, []byte(strings.Repeat("a", 64))) {
		t.Fatalf("ledger contains content or account material: %v", err)
	}
	for _, bad := range []string{path + "&limit=1001", path + "&limit=-1", path + "&limit=", path + "&before=garbage", path + "&before=", localDouyinExecutionRoundsPath + "?date=2026-02-30"} {
		if code, _ := f.request("GET", bad, nil, false); code != http.StatusBadRequest {
			t.Fatalf("accepted invalid query: %s %d", bad, code)
		}
	}
	view = setSyntheticNoticeAgent(t, f, true)
	if code, out := f.request("GET", path, nil, false); code != http.StatusOK || len(out["rounds"].([]any)) != 3 {
		t.Fatalf("unrelated notice tool grant hid history: %d %v", code, out)
	}
	if code, out := read(prepare()["ticket"]); code != http.StatusOK || out["rounds_recorded"] != true || reads != 3 {
		t.Fatalf("notice after grant change: %d %v reads=%d", code, out, reads)
	}
	if code, out := f.request("GET", path, nil, false); code != http.StatusOK || len(out["rounds"].([]any)) != 3 {
		t.Fatalf("same notice duplicated after grant change: %d %v", code, out)
	}
	code, revoked := f.request("POST", localDouyinNoticePath, map[string]any{"action": "revoke", "revision": view["revision"]}, true)
	if code != http.StatusOK {
		t.Fatalf("revoke: %d %v", code, revoked)
	}
	if code, _ := f.request("GET", path, nil, false); code != http.StatusForbidden || reads != 3 {
		t.Fatalf("revoked ledger visible or platform touched: %d reads=%d", code, reads)
	}
	code, rebound := f.request("POST", localDouyinNoticePath, map[string]any{"action": "bind", "revision": revoked["revision"]}, true)
	if code != http.StatusOK {
		t.Fatalf("rebind: %d %v", code, rebound)
	}
	if code, out := f.request("GET", path, nil, false); code != http.StatusOK || len(out["rounds"].([]any)) != 0 {
		t.Fatalf("older binding ledger reopened: %d %v", code, out)
	}
}

func TestLocalDouyinExecutionRoundsManualWriteFailureKeepsReadResult(t *testing.T) {
	f, m, view := syntheticExecutionRoundFixture(t, true)
	view = grantExecutionNoticeMemory(t, f, view)
	reads := 0
	m.readNotice = func(_ context.Context, _ string, in stationbrowser.DouyinNoticeRequest, check func() error) (stationbrowser.DouyinNoticePage, error) {
		reads++
		if err := check(); err != nil {
			return stationbrowser.DouyinNoticePage{}, err
		}
		return stationbrowser.DouyinNoticePage{Account: in.Account, PagesRead: 1, HasMore: true,
			Next:         stationbrowser.DouyinNoticeCheckpoint{Pages: 1, Digest: [32]byte{1}},
			RenderedText: "合成已读正文", Items: []stationbrowser.DouyinNotice{{ID: "not-saved", Type: 9}}}, nil
	}
	blocker := filepath.Join(stationApplicationsDir(f.dir), "douyin", "execution-rounds")
	if err := os.WriteFile(blocker, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	code, prepared := f.request("POST", localDouyinNoticePath, map[string]any{"action": "prepare", "revision": view["revision"], "allow_mark_read": true}, true)
	if code != http.StatusOK {
		t.Fatal(code, prepared)
	}
	payload := map[string]any{"action": "read", "revision": view["revision"], "allow_mark_read": true, "ticket": prepared["ticket"]}
	code, out := f.request("POST", localDouyinNoticePath, payload, true)
	if code != http.StatusOK || out["rounds_recorded"] != false || out["memory_recorded"] != true || out["continuation"] == "" || out["rendered_text"] != "合成已读正文" || out["rounds_warning"] == "" || reads != 1 {
		t.Fatalf("write failure swallowed read result: %d %v reads=%d", code, out, reads)
	}
	db, err := openLocalDouyinMemory(t.Context(), m.home, false)
	if err != nil {
		t.Fatal("ledger failure blocked independent notice memory", err)
	}
	defer db.Close()
	var saved int
	if err := db.QueryRow(`SELECT count(*) FROM records WHERE text=?`, "合成已读正文").Scan(&saved); err != nil || saved != 1 {
		t.Fatalf("notice memory not saved after ledger failure: %d %v", saved, err)
	}
	if code, _ := f.request("POST", localDouyinNoticePath, payload, true); code != http.StatusConflict || reads != 1 {
		t.Fatalf("used read ticket replayed: %d reads=%d", code, reads)
	}
}

func TestLocalDouyinExecutionRoundsMemoryFailureKeepsLedgerAndReadResult(t *testing.T) {
	f, m, view := syntheticExecutionRoundFixture(t, true)
	view = grantExecutionNoticeMemory(t, f, view)
	m.readNotice = func(_ context.Context, _ string, in stationbrowser.DouyinNoticeRequest, check func() error) (stationbrowser.DouyinNoticePage, error) {
		if err := check(); err != nil {
			return stationbrowser.DouyinNoticePage{}, err
		}
		return stationbrowser.DouyinNoticePage{Account: in.Account, PagesRead: 1, RenderedText: "合成记忆失败正文",
			Items: []stationbrowser.DouyinNotice{{ID: "memory-failed", Type: 3}}}, nil
	}
	blocker := filepath.Join(stationApplicationsDir(f.dir), "douyin", "memory")
	if err := os.WriteFile(blocker, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	code, prepared := f.request("POST", localDouyinNoticePath, map[string]any{"action": "prepare", "revision": view["revision"], "allow_mark_read": true}, true)
	if code != http.StatusOK {
		t.Fatal(code, prepared)
	}
	code, out := f.request("POST", localDouyinNoticePath, map[string]any{"action": "read", "revision": view["revision"], "allow_mark_read": true, "ticket": prepared["ticket"]}, true)
	if code != http.StatusOK || out["rounds_recorded"] != true || out["memory_recorded"] != false || out["memory_warning"] == "" || out["rendered_text"] != "合成记忆失败正文" {
		t.Fatalf("memory failure swallowed read result: %d %v", code, out)
	}
	if code, page := f.request("GET", localDouyinExecutionRoundsPath+"?date="+executionRoundDay(), nil, false); code != http.StatusOK || len(page["rounds"].([]any)) != 1 {
		t.Fatalf("memory failure discarded execution ledger: %d %v", code, page)
	}
}

func TestLocalDouyinExecutionRoundsInboxReadAndDurableWriteFailure(t *testing.T) {
	f, m, _ := syntheticExecutionRoundFixture(t, true)
	reads := 0
	m.readNotice = func(_ context.Context, _ string, in stationbrowser.DouyinNoticeRequest, check func() error) (stationbrowser.DouyinNoticePage, error) {
		reads++
		if err := check(); err != nil {
			return stationbrowser.DouyinNoticePage{}, err
		}
		return stationbrowser.DouyinNoticePage{Account: in.Account, PagesRead: 1, RenderedText: "聊天私有通知正文", Items: []stationbrowser.DouyinNotice{{ID: "inbox-1", Type: 42}}}, nil
	}
	tools := noticeToolSet(t, f, "execution-inbox", true)
	if _, err := tools.Call(t.Context(), localInboxToolName, inboxArgs(nil)); err != nil || reads != 1 {
		t.Fatalf("inbox read: %v reads=%d", err, reads)
	}
	path := localDouyinExecutionRoundsPath + "?date=" + executionRoundDay()
	if code, out := f.request("GET", path, nil, false); code != http.StatusOK || len(out["rounds"].([]any)) != 1 {
		t.Fatalf("inbox notice absent from ledger: %d %v", code, out)
	}
	// A failed durable write must be visible to the caller. The consumed tool
	// operation must not repeat a possibly read and marked notification.
	f, m, _ = syntheticExecutionRoundFixture(t, true)
	reads = 0
	m.readNotice = func(_ context.Context, _ string, in stationbrowser.DouyinNoticeRequest, check func() error) (stationbrowser.DouyinNoticePage, error) {
		reads++
		if err := check(); err != nil {
			return stationbrowser.DouyinNoticePage{}, err
		}
		return stationbrowser.DouyinNoticePage{Account: in.Account, PagesRead: 1, RenderedText: "未入账私有正文", Items: []stationbrowser.DouyinNotice{{ID: "inbox-failed"}}}, nil
	}
	blocker := filepath.Join(stationApplicationsDir(f.dir), "douyin", "execution-rounds")
	if err := os.WriteFile(blocker, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	failed := noticeToolSet(t, f, "execution-inbox-failed", true)
	if value, err := failed.Call(t.Context(), localInboxToolName, inboxArgs(nil)); err != nil || !strings.Contains(value, `"rounds_recorded":false`) || !strings.Contains(value, "执行回合记录未确认保存") || reads != 1 {
		t.Fatalf("write failure hidden or platform result lost: %q %v reads=%d", value, err, reads)
	}
	if value, err := failed.Call(t.Context(), localInboxToolName, inboxArgs(nil)); err != nil || !strings.Contains(value, "read_not_repeated") || reads != 1 {
		t.Fatalf("unknown result was repeated: %q %v reads=%d", value, err, reads)
	}
}
