package station

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/stationbrowser"
)

func TestLocalDouyinCreatorWorksSeparateGrantListDetailsAndRevocation(t *testing.T) {
	f, manager := newLocalDouyinWebFixture(t)
	initializeDouyinWeb(t, f)
	_, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "begin", "revision": 0, "approve_profile": true}, true)
	_, view = f.request("POST", localDouyinWebRoute, map[string]any{"action": "confirm", "revision": view["revision"], "attempt_id": view["attempt_id"]}, true)
	manager.readWorks = func(_ context.Context, _ string, in stationbrowser.DouyinCreatorWorksRequest, authorize func() error) (stationbrowser.DouyinCreatorWorksPage, error) {
		if err := authorize(); err != nil {
			return stationbrowser.DouyinCreatorWorksPage{}, err
		}
		if in.Page == 1 {
			if in.PreviousAccount != "account-proof" || in.PreviousPageKey != "first-proof" {
				t.Errorf("lost page binding: %+v", in)
			}
			return stationbrowser.DouyinCreatorWorksPage{Account: "account-proof", PageKey: "second-proof", CoverageKnown: true, Works: []stationbrowser.DouyinCreatorWork{
				{ID: "7391234567890123458", Title: "第二页", Visibility: "公开"},
			}}, nil
		}
		return stationbrowser.DouyinCreatorWorksPage{Account: "account-proof", PageKey: "first-proof", CoverageKnown: true, HasMore: true, Works: []stationbrowser.DouyinCreatorWork{
			{ID: "7391234567890123456", Title: "私密合成作品", Caption: "私密正文只在详细读取返回", Visibility: "仅自己可见"},
			{ID: "7391234567890123457", Title: "另一个作品", Visibility: "公开", Video: true},
		}}, nil
	}
	const path = "/api/local/apps/observation"
	list := map[string]any{"app_id": "douyin", "connection_id": douyinapp.WebReadConnectionID, "content_id": douyinapp.WebWorksContentID, "field": "account_works", "limit": 1}
	if code, _ := f.request("POST", path, list, true); code != 403 {
		t.Fatalf("list exposed before authorization: %d", code)
	}
	code, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_works", "revision": view["revision"], "allow_works": true}, true)
	if code != 200 || view["works_enabled"] != true || view["read_enabled"] != false || view["publish_enabled"] != false {
		t.Fatalf("works changed unrelated grants: %d %v", code, view)
	}
	code, targets := f.request(http.MethodGet, path+"?app_id=douyin", nil, false)
	items := localAppObservationItems(targets)
	if code != 200 || len(items) != 1 || items[0].(map[string]any)["content_id"] != douyinapp.WebWorksContentID {
		t.Fatalf("works discovery: %d %v", code, targets)
	}
	code, result := f.request("POST", path, list, true)
	if code != 200 || result["complete"] != false || len(result["chunks"].([]any)) != 1 {
		t.Fatalf("works first page: %d %v", code, result)
	}
	row := result["chunks"].([]any)[0].(map[string]any)
	if strings.Contains(row["text"].(string), "私密正文") {
		t.Fatal("list exposed detailed body")
	}
	detail := map[string]any{"app_id": "douyin", "connection_id": douyinapp.WebReadConnectionID, "content_id": row["follow_up_content_id"], "field": "page_text"}
	code, details := f.request("POST", path, detail, true)
	if code != 200 || !strings.Contains(details["chunks"].([]any)[0].(map[string]any)["text"].(string), "私密正文") {
		t.Fatalf("bound work detail: %d %v", code, details)
	}
	detail["field"] = "visual"
	if code, _ := f.request("POST", path, detail, true); code != 403 {
		t.Fatalf("works granted visual: %d", code)
	}
	if code, second := f.request("POST", path, list, true); code != 200 || second["chunks"].([]any)[0].(map[string]any)["source_id"] != "7391234567890123457" {
		t.Fatalf("local continuation: %d %v", code, second)
	}
	if code, third := f.request("POST", path, list, true); code != 200 || third["chunks"].([]any)[0].(map[string]any)["source_id"] != "7391234567890123458" || third["complete"] != true {
		t.Fatalf("platform continuation: %d %v", code, third)
	}
	if code, _ := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_works", "revision": view["revision"], "allow_works": false}, true); code != 200 {
		t.Fatalf("revoke works: %d", code)
	}
	detail["field"] = "page_text"
	if code, _ := f.request("POST", path, detail, true); code != 403 {
		t.Fatalf("revoked detail: %d", code)
	}
}

func TestLocalDouyinCreatorCommentReplyEvidenceSurvivesResolverRestart(t *testing.T) {
	f, m, _, view := syntheticInteractionFixture(t, true)
	code, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_works", "revision": view["revision"], "allow_works": true}, true)
	if code != 200 {
		t.Fatalf("enable own works: %d %v", code, view)
	}
	m.readWorks = func(_ context.Context, _ string, _ stationbrowser.DouyinCreatorWorksRequest, authorize func() error) (stationbrowser.DouyinCreatorWorksPage, error) {
		if err := authorize(); err != nil {
			return stationbrowser.DouyinCreatorWorksPage{}, err
		}
		return stationbrowser.DouyinCreatorWorksPage{Account: "synthetic-owner", PageKey: "synthetic-page", CoverageKnown: true, Works: []stationbrowser.DouyinCreatorWork{
			{ID: "7391234567890123456", Title: "合成公开作品", Visibility: "公开", Video: true},
			{ID: "7391234567890123457", Title: "合成私密作品", Visibility: "仅自己可见", Video: true},
		}}, nil
	}
	m.readComments = func(_ context.Context, _ string, input stationbrowser.DouyinCommentsRequest, authorize func() error) (stationbrowser.DouyinCommentPage, error) {
		if input.VideoID != "7391234567890123456" {
			t.Errorf("wrong video admitted: %s", input.VideoID)
		}
		if err := authorize(); err != nil {
			return stationbrowser.DouyinCommentPage{}, err
		}
		return stationbrowser.DouyinCommentPage{Complete: true, Comments: []stationbrowser.DouyinComment{{ID: "123", Author: "合成", AuthorID: "synthetic", Text: "合成原评论"}}}, nil
	}
	const path = "/api/local/apps/observation"
	read := map[string]any{"app_id": "douyin", "connection_id": douyinapp.WebReadConnectionID, "content_id": douyinapp.WebWorksContentID, "field": "account_works"}
	code, result := f.request("POST", path, read, true)
	if code != 200 || len(result["chunks"].([]any)) != 2 {
		t.Fatalf("works list: %d %v", code, result)
	}
	chunks := result["chunks"].([]any)
	own := chunks[0].(map[string]any)["follow_up_content_id"].(string)
	private := chunks[1].(map[string]any)["follow_up_content_id"].(string)
	entry, _ := f.handler.chat.applications.Lookup("douyin")
	restarted := &localDouyinWebInteraction{home: f.handler.home, installation: entry.InstallationID}
	access, err := restarted.access(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	draft := apphost.InteractionDraft{Target: restarted.ref(access), ContentID: own, Kind: "reply_comment", CommentID: "123", Text: "合成回复"}
	if _, err := restarted.ResolveInteractionSubject(t.Context(), draft); err == nil {
		t.Fatal("unread creator comment admitted")
	}
	read["field"], read["content_id"] = "comments", private
	if code, _ := f.request("POST", path, read, true); code != 403 {
		t.Fatalf("private creator comments exposed: %d", code)
	}
	read["content_id"] = own
	if code, result := f.request("POST", path, read, true); code != 200 {
		t.Fatalf("creator comments: %d %v", code, result)
	}
	draft.CommentVersion, err = restarted.ResolveInteractionSubject(t.Context(), draft)
	if err != nil || draft.CommentVersion == "" {
		t.Fatalf("persisted comment evidence: %v", err)
	}
	grant, err := restarted.GrantInteraction(t.Context(), draft)
	if err != nil || !grant.Allowed {
		t.Fatalf("grant after resolver restart: %+v %v", grant, err)
	}
	if code, _ := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_works", "revision": view["revision"], "allow_works": false}, true); code != 200 {
		t.Fatalf("revoke works: %d", code)
	}
	if grant, _ := restarted.GrantInteraction(t.Context(), draft); grant.Allowed {
		t.Fatal("revoked creator evidence still authorized a reply")
	}
}

func TestLocalDouyinCreatorAccountSwitchInvalidatesDurableTargets(t *testing.T) {
	f, manager, _, view := syntheticInteractionFixture(t, true)
	if code, out := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_works", "revision": view["revision"], "allow_works": true}, true); code != 200 {
		t.Fatal(code, out)
	}
	const video = "7391234567890123456"
	account, visibility := "synthetic-original-owner", "公开"
	worksReads := 0
	readWorks := func(_ context.Context, _ string, _ stationbrowser.DouyinCreatorWorksRequest, authorize func() error) (stationbrowser.DouyinCreatorWorksPage, error) {
		worksReads++
		return stationbrowser.DouyinCreatorWorksPage{Account: account, PageKey: "same-video-id", CoverageKnown: true,
			Works: []stationbrowser.DouyinCreatorWork{{ID: video, Title: "合成作品", Visibility: visibility, Video: true}}}, authorize()
	}
	manager.readWorks = readWorks
	manager.readComments = func(_ context.Context, _ string, request stationbrowser.DouyinCommentsRequest, authorize func() error) (stationbrowser.DouyinCommentPage, error) {
		if request.VideoID != video {
			t.Error("wrong source video")
		}
		return stationbrowser.DouyinCommentPage{Complete: true, Comments: []stationbrowser.DouyinComment{
			{ID: "123", Author: "合成", AuthorID: "synthetic", Text: "合成评论"},
		}}, authorize()
	}
	const path = "/api/local/apps/observation"
	list := map[string]any{"app_id": "douyin", "connection_id": douyinapp.WebReadConnectionID, "content_id": douyinapp.WebWorksContentID, "field": "account_works", "refresh": true}
	code, page := f.request("POST", path, list, true)
	if code != 200 {
		t.Fatal(code, page)
	}
	content := page["chunks"].([]any)[0].(map[string]any)["follow_up_content_id"].(string)
	comments := map[string]any{"app_id": "douyin", "connection_id": douyinapp.WebReadConnectionID, "content_id": content, "field": "comments"}
	if code, out := f.request("POST", path, comments, true); code != 200 {
		t.Fatal(code, out)
	}
	f.server.Close()
	if err := f.handler.Close(); err != nil {
		t.Fatal(err)
	}
	registry, err := apphost.New(douyinapp.New(filepath.Join(f.dir, "apps", "douyin")).Registration())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewLocalHomeHandlerWithApps(f.dir, f.root, "synthetic-local-password", "", registry)
	if err != nil {
		t.Fatal(err)
	}
	f = newLocalAppObservationFixtureWithHandler(t, handler, f.dir, f.root)
	f.login()
	f.handler.home.douyinWeb.available = func() bool { return true }
	f.handler.home.douyinWeb.readWorks = readWorks
	commentReads := 0
	f.handler.home.douyinWeb.readComments = func(context.Context, string, stationbrowser.DouyinCommentsRequest, func() error) (stationbrowser.DouyinCommentPage, error) {
		commentReads++
		return stationbrowser.DouyinCommentPage{}, nil
	}
	entry, _ := f.handler.chat.applications.Lookup("douyin")
	provider := &localDouyinWebInteraction{home: f.handler.home, installation: entry.InstallationID}
	access, err := provider.access(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	draft := apphost.InteractionDraft{Target: provider.ref(access), ContentID: content, Kind: "reply_comment", CommentID: "123"}
	if _, err := provider.ResolveInteractionSubject(t.Context(), draft); err != nil {
		t.Fatal("unchanged creator proof did not survive restart", err)
	}
	visibility = "仅自己可见"
	if code, out := f.request("POST", path, comments, true); code != 409 || commentReads != 0 {
		t.Fatal("private work reached the public comments reader", code, out, commentReads)
	}
	account, visibility = "synthetic-other-owner", "公开"
	if code, out := f.request("POST", path, comments, true); code != 409 || out["chunks"] != nil || commentReads != 0 {
		t.Fatal("switched account reached the comments reader after restart", code, out, commentReads)
	}
	if _, err := provider.ResolveInteractionSubject(t.Context(), draft); err == nil {
		t.Fatal("old comment remained an authorized reply target")
	}
	account = "synthetic-original-owner"
	priorReads := worksReads
	if code, out := f.request("POST", path, list, true); code != 409 || out["chunks"] != nil || worksReads != priorReads {
		t.Fatal("account change was silently reset", code, out)
	}
}
