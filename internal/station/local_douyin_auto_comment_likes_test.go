package station

import (
	"context"
	"net/http"
	"testing"

	"wechat-bot/internal/stationbrowser"
)

func TestLocalDouyinAutoCommentLikesRunPersistsUnknownAndStaysPartial(t *testing.T) {
	f, m := newLocalDouyinWebFixture(t)
	initializeDouyinWeb(t, f)
	_, view := f.request(http.MethodPost, localDouyinWebRoute, map[string]any{"action": "begin", "revision": 0, "approve_profile": true}, true)
	_, view = f.request(http.MethodPost, localDouyinWebRoute, map[string]any{"action": "confirm", "revision": view["revision"], "attempt_id": view["attempt_id"]}, true)
	if code, status := f.request(http.MethodGet, localDouyinAutoCommentLikesPath, nil, false); code != 200 || status["enabled"] != false || status["coverage_state"] != "unsupported" {
		t.Fatalf("disabled status: %d %v", code, status)
	}
	const account = "account-proof"
	const workID = "7391234567890123457"
	identity, err := f.handler.home.identity.Read()
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := m.noticeState(identity.FishID)
	if err != nil {
		t.Fatal(err)
	}
	a := (&localDouyinWebObservation{home: m.home}).access(identity.FishID, identity.LocalOwner.ID, record, localDouyinWebView{})
	if err := (&localDouyinWebObservation{home: m.home}).admitCreatorAccount(t.Context(), a, account); err != nil {
		t.Fatal(err)
	}
	work := stationbrowser.DouyinCreatorWork{ID: workID, Title: "合成公开作品", Visibility: "公开", Video: true}
	page := stationbrowser.DouyinCreatorWorksPage{Account: account, PageKey: "page-proof", CoverageKnown: true, Works: []stationbrowser.DouyinCreatorWork{
		work,
		{ID: "7391234567890123460", Title: "合成图文", Visibility: "公开", Images: []string{"synthetic-image"}},
		{ID: "7391234567890123461", Title: "合成私密作品", Visibility: "仅自己可见", Video: true},
	}}
	if err := m.captureWorksCatalog(t.Context(), a, localCreatorWorksCursor{}, page, page.Works, len(page.Works)); err != nil {
		t.Fatal(err)
	}
	code, view := f.request(http.MethodPost, localDouyinWebRoute, map[string]any{"action": "set_auto_comment_likes", "revision": view["revision"], "allow_auto_comment_likes": true}, true)
	if code != 200 || view["auto_comment_likes_enabled"] != true {
		t.Fatalf("enable auto comment likes: %d %v", code, view)
	}
	m.readWorks = func(_ context.Context, _ string, _ stationbrowser.DouyinCreatorWorksRequest, authorize func() error) (stationbrowser.DouyinCreatorWorksPage, error) {
		if err := authorize(); err != nil {
			return stationbrowser.DouyinCreatorWorksPage{}, err
		}
		return page, nil
	}
	m.readComments = func(_ context.Context, _ string, req stationbrowser.DouyinCommentsRequest, authorize func() error) (stationbrowser.DouyinCommentPage, error) {
		if err := authorize(); err != nil {
			return stationbrowser.DouyinCommentPage{}, err
		}
		if req.VideoID != workID || req.Cursor != "" || req.Limit != 20 {
			t.Fatalf("wrong comment request: %+v", req)
		}
		return stationbrowser.DouyinCommentPage{Complete: true, Comments: []stationbrowser.DouyinComment{
			{ID: "7391234567890123458", AuthorID: "self-sec-uid", Text: "自己的评论"},
			{ID: "7391234567890123459", AuthorID: "other-sec-uid", Text: "其他人的评论"},
		}}, nil
	}
	var calls int
	m.commentLike = func(ctx context.Context, _ string, req stationbrowser.DouyinCommentLikeRequest, authorize func() error) (stationbrowser.DouyinCommentLikeResult, error) {
		calls++
		if err := authorize(); err != nil {
			return stationbrowser.DouyinCommentLikeResult{}, err
		}
		if req.ExpectedAuthorID == "self-sec-uid" {
			return stationbrowser.DouyinCommentLikeResult{State: "applied", Code: "already_applied"}, nil
		}
		if err := req.BeforeSubmit(ctx); err != nil {
			return stationbrowser.DouyinCommentLikeResult{}, err
		}
		return stationbrowser.DouyinCommentLikeResult{State: "unknown", Code: "receipt_unknown", Submitted: true}, nil
	}
	run := map[string]any{"action": "run", "revision": view["revision"], "work_id": workID}
	code, result := f.request(http.MethodPost, localDouyinAutoCommentLikesPath, run, true)
	if code != 200 || result["enabled"] != true || result["coverage_state"] != "partial" || result["main_list_end"] != true || result["seen"] != float64(2) ||
		result["eligible_works"] != float64(1) || len(result["uncovered_works"].([]any)) != 2 || calls != 2 {
		t.Fatalf("first run: %d %v calls=%d", code, result, calls)
	}
	counts := result["works"].(map[string]any)[workID].(map[string]any)
	if counts["applied"] != float64(1) || counts["unknown"] != float64(1) {
		t.Fatalf("intent counts: %v", counts)
	}
	code, result = f.request(http.MethodPost, localDouyinAutoCommentLikesPath, run, true)
	if code != 200 || result["skipped"] != float64(2) || calls != 2 {
		t.Fatalf("repeated run submitted a duplicate: %d %v calls=%d", code, result, calls)
	}
	m.readComments = func(_ context.Context, _ string, req stationbrowser.DouyinCommentsRequest, authorize func() error) (stationbrowser.DouyinCommentPage, error) {
		if err := authorize(); err != nil {
			return stationbrowser.DouyinCommentPage{}, err
		}
		if req.Cursor == "" {
			return stationbrowser.DouyinCommentPage{Comments: []stationbrowser.DouyinComment{{ID: "7391234567890123458", AuthorID: "self-sec-uid", Text: "自己的评论"}}, NextCursor: "7391234567890123458"}, nil
		}
		if req.Cursor == "7391234567890123458" {
			return stationbrowser.DouyinCommentPage{Complete: true, Comments: []stationbrowser.DouyinComment{{ID: "7391234567890123459", AuthorID: "other-sec-uid", Text: "其他人的评论"}}}, nil
		}
		t.Fatalf("unexpected background cursor: %q", req.Cursor)
		return stationbrowser.DouyinCommentPage{}, nil
	}
	if err := m.autoCommentLikeStep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if code, status := f.request(http.MethodGet, localDouyinAutoCommentLikesPath, nil, false); code != 200 || status["scan_work_id"] != workID || status["scan_cursor"] != "7391234567890123458" {
		t.Fatalf("background cursor was not persisted: %d %v", code, status)
	}
	if err := m.autoCommentLikeStep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if code, status := f.request(http.MethodGet, localDouyinAutoCommentLikesPath, nil, false); code != 200 || status["scan_work_id"] != workID || status["scan_cursor"] != nil {
		t.Fatalf("background final page did not wrap: %d %v", code, status)
	}
	_, view = f.request(http.MethodPost, localDouyinWebRoute, map[string]any{"action": "pause", "revision": view["revision"]}, true)
	if code, result := f.request(http.MethodPost, localDouyinAutoCommentLikesPath, map[string]any{"action": "run", "revision": view["revision"], "work_id": workID}, true); code != http.StatusConflict || result["error"] == nil || calls != 2 {
		t.Fatalf("paused run escaped gate: %d %v calls=%d", code, result, calls)
	}
}
