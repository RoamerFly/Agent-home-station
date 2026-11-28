package station

import (
	"context"
	"errors"
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
	if code != 200 || view["auto_comment_likes_enabled"] != true || view["likes_enabled"] != false {
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
	var preSubmitFailure, failureArrived bool
	m.commentLike = func(ctx context.Context, _ string, req stationbrowser.DouyinCommentLikeRequest, authorize func() error) (stationbrowser.DouyinCommentLikeResult, error) {
		calls++
		if err := authorize(); err != nil {
			return stationbrowser.DouyinCommentLikeResult{}, err
		}
		if req.ExpectedAuthorID == "self-sec-uid" {
			return stationbrowser.DouyinCommentLikeResult{State: "applied", Code: "already_applied"}, nil
		}
		if req.CommentID == "7391234567890123464" && preSubmitFailure {
			return stationbrowser.DouyinCommentLikeResult{}, errors.New("synthetic pre-submit observation failure")
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
	m.commentLikePause = func(context.Context) error { return nil }
	newArrived := false
	m.readComments = func(_ context.Context, _ string, req stationbrowser.DouyinCommentsRequest, authorize func() error) (stationbrowser.DouyinCommentPage, error) {
		if err := authorize(); err != nil {
			return stationbrowser.DouyinCommentPage{}, err
		}
		if req.RootCommentID == "7391234567890123458" {
			return stationbrowser.DouyinCommentPage{Complete: true, Comments: []stationbrowser.DouyinComment{
				{ID: "7391234567890123462", RootCommentID: req.RootCommentID, AuthorID: "other-sec-uid", Text: "旧子评论"},
			}}, nil
		}
		if req.RootCommentID == "7391234567890123459" || req.RootCommentID == "7391234567890123463" {
			return stationbrowser.DouyinCommentPage{Complete: true}, nil
		}
		if newArrived {
			comments := []stationbrowser.DouyinComment{
				{ID: "7391234567890123458", AuthorID: "self-sec-uid", Text: "自己的评论"},
				{ID: "7391234567890123459", AuthorID: "other-sec-uid", Text: "其他人的评论"},
				{ID: "7391234567890123463", AuthorID: "other-sec-uid", Text: "新主评论"},
			}
			if failureArrived {
				comments = append(comments, stationbrowser.DouyinComment{ID: "7391234567890123464", AuthorID: "other-sec-uid", Text: "提交前失败的评论"})
			}
			return stationbrowser.DouyinCommentPage{Complete: true, Comments: comments}, nil
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
	for i := 0; i < 4; i++ {
		if err := m.autoCommentLikeStep(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if code, status := f.request(http.MethodGet, localDouyinAutoCommentLikesPath, nil, false); code != 200 ||
		status["auto"].(map[string]any)["state"] != "active" || status["scan_work_id"] != workID ||
		status["scan_cursor"] != nil || calls != 2 {
		t.Fatalf("baseline should finish without liking old main/reply comments: %d %v calls=%d", code, status, calls)
	}
	newArrived = true
	if err := m.autoCommentLikeStep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("new main comment was not liked exactly once: calls=%d", calls)
	}
	code, view = f.request(http.MethodPost, localDouyinWebRoute, map[string]any{
		"action": "set_auto_comment_likes", "revision": view["revision"], "allow_auto_comment_likes": false,
	}, true)
	if code != 200 || view["auto_comment_likes_enabled"] != false {
		t.Fatalf("disable auto mode: %d %v", code, view)
	}
	batch, code, err := m.commentLikeBatchSet(t.Context(), identity.FishID, identity.LocalOwner.ID, true)
	if err != nil || code != 200 || batch.Batch.State != "running" || batch.Enabled || view["likes_enabled"] != false {
		t.Fatalf("historical batch must start independently: %d %+v %v", code, batch.Batch, err)
	}
	if did, err := m.commentLikeStep(t.Context(), "batch"); err != nil || !did {
		t.Fatalf("batch first page: did=%v err=%v", did, err)
	}
	batch, code, err = m.commentLikeBatchSet(t.Context(), identity.FishID, identity.LocalOwner.ID, false)
	if err != nil || code != 200 || batch.Batch.State != "cancelled" {
		t.Fatalf("cancel batch: %d %+v %v", code, batch.Batch, err)
	}
	if did, err := m.commentLikeStep(t.Context(), "batch"); err != nil || did {
		t.Fatalf("cancelled batch continued: did=%v err=%v", did, err)
	}
	batch, code, err = m.commentLikeBatchSet(t.Context(), identity.FishID, identity.LocalOwner.ID, true)
	if err != nil || code != 200 || batch.Batch.State != "running" || batch.Batch.RootCommentID != "7391234567890123458" {
		t.Fatalf("resume batch lost reply position: %d %+v %v", code, batch.Batch, err)
	}
	if did, err := m.commentLikeStep(t.Context(), "batch"); err != nil || !did || calls != 4 {
		t.Fatalf("batch did not process historical child exactly once: did=%v err=%v calls=%d", did, err, calls)
	}
	for i := 0; i < 2; i++ {
		if did, err := m.commentLikeStep(t.Context(), "batch"); err != nil || !did {
			t.Fatalf("batch reply completion %d: did=%v err=%v", i, did, err)
		}
	}
	if code, result := f.request(http.MethodGet, localDouyinAutoCommentLikesPath, nil, false); code != 200 ||
		result["batch"].(map[string]any)["state"] != "complete" ||
		result["batch"].(map[string]any)["completed_works"] != float64(1) || calls != 4 {
		t.Fatalf("batch completion: %d %v calls=%d", code, result, calls)
	}
	failureArrived, preSubmitFailure = true, true
	if _, code, err := m.commentLikeBatchSet(t.Context(), identity.FishID, identity.LocalOwner.ID, true); err != nil || code != 200 {
		t.Fatalf("restart batch for new comments: %d %v", code, err)
	}
	if did, err := m.commentLikeStep(t.Context(), "batch"); !did || err == nil || calls != 5 {
		t.Fatalf("pre-submit failure should stop current page: did=%v err=%v calls=%d", did, err, calls)
	}
	if code, result := f.request(http.MethodGet, localDouyinAutoCommentLikesPath, nil, false); code != 200 ||
		result["batch"].(map[string]any)["state"] != "error" ||
		result["works"].(map[string]any)[workID].(map[string]any)["failed"] != float64(1) {
		t.Fatalf("pre-submit failure was not persisted: %d %v", code, result)
	}
	preSubmitFailure = false
	if _, code, err := m.commentLikeBatchSet(t.Context(), identity.FishID, identity.LocalOwner.ID, true); err != nil || code != 200 {
		t.Fatalf("resume failed batch: %d %v", code, err)
	}
	if did, err := m.commentLikeStep(t.Context(), "batch"); err != nil || !did || calls != 6 {
		t.Fatalf("retry after pre-submit failure: did=%v err=%v calls=%d", did, err, calls)
	}
	_, view = f.request(http.MethodPost, localDouyinWebRoute, map[string]any{"action": "pause", "revision": view["revision"]}, true)
	if code, result := f.request(http.MethodPost, localDouyinAutoCommentLikesPath, map[string]any{"action": "run", "revision": view["revision"], "work_id": workID}, true); code != http.StatusConflict || result["error"] == nil || calls != 6 {
		t.Fatalf("paused run escaped gate: %d %v calls=%d", code, result, calls)
	}
}

func TestRandomDouyinCommentLikePauseCanCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := randomDouyinCommentLikePause(ctx); err != context.Canceled {
		t.Fatalf("cancelled pacing must stop before the next like: %v", err)
	}
}
