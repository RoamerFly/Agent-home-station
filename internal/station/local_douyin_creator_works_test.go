package station

import (
	"context"
	"net/http"
	"strings"
	"testing"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
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
