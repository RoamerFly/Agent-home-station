package station

import (
	"context"
	"fmt"
	"strings"
	"testing"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/stationbrowser"
)

func TestLocalDouyinCreatorImagesRequiresCurrentOwnedWork(t *testing.T) {
	f, manager := newLocalDouyinWebFixture(t)
	initializeDouyinWeb(t, f)
	_, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "begin", "revision": 0, "approve_profile": true}, true)
	_, view = f.request("POST", localDouyinWebRoute, map[string]any{"action": "confirm", "revision": view["revision"], "attempt_id": view["attempt_id"]}, true)
	owner, visibility := "synthetic-owner", "公开"
	reads, downloads, visions := 0, 0, 0
	reordered := false
	manager.readWorks = func(_ context.Context, _ string, _ stationbrowser.DouyinCreatorWorksRequest, authorize func() error) (stationbrowser.DouyinCreatorWorksPage, error) {
		if err := authorize(); err != nil {
			return stationbrowser.DouyinCreatorWorksPage{}, err
		}
		reads++
		urls := make([]string, 3)
		for i := range urls {
			urls[i] = fmt.Sprintf("https://fixture.douyinpic.com/gallery/%d?test-signature=%d", i, reads)
		}
		if reordered {
			urls[1], urls[2] = urls[2], urls[1]
		}
		return stationbrowser.DouyinCreatorWorksPage{Account: owner, PageKey: "synthetic-page", CoverageKnown: true, Works: []stationbrowser.DouyinCreatorWork{
			{ID: "7391234567890123456", Title: "合成图文", Visibility: visibility, Images: urls},
			{ID: "7391234567890123457", Title: "私密图文", Visibility: "仅自己可见", Images: urls},
		}}, nil
	}
	manager.readImages = func(_ context.Context, urls []string, authorize func() error) ([]stationbrowser.DouyinWebMedia, error) {
		if err := authorize(); err != nil {
			return nil, err
		}
		downloads++
		result := make([]stationbrowser.DouyinWebMedia, 0, len(urls))
		for _, url := range urls {
			path, query, _ := strings.Cut(url, "?")
			if query != fmt.Sprintf("test-signature=%d", reads) {
				t.Errorf("download reused old URL: %s", url)
			}
			result = append(result, stationbrowser.DouyinWebMedia{ContentType: "image/png", Data: []byte(path[len(path)-1:])})
		}
		return result, nil
	}
	manager.imageVision = stationLocalVision{
		version: func(context.Context) (string, error) { return "synthetic-image-vision", nil },
		observe: func(_ context.Context, source, prompt string, images []deepseek.Image, _ int, authorize func() error) (string, error) {
			if err := authorize(); err != nil {
				return "", err
			}
			visions++
			var positions strings.Builder
			for _, image := range images {
				positions.Write(image.Data)
			}
			if !strings.HasPrefix(source, "app-visual:") || !strings.Contains(prompt, "公开图文") {
				t.Errorf("vision missing bound image prompt: %s %s", source, prompt)
			}
			return "图片位置 " + positions.String(), nil
		},
	}
	permit := func(action, key string, allowed bool, expected int) {
		t.Helper()
		code, next := f.request("POST", localDouyinWebRoute, map[string]any{"action": action, "revision": view["revision"], key: allowed}, true)
		if code != expected {
			t.Fatalf("%s: %d %v", action, code, next)
		}
		if code == 200 {
			view = next
		}
	}
	permit("set_images", "allow_images", true, 409)
	permit("set_works", "allow_works", true, 200)
	if view["images_ready"] != true || view["images_enabled"] != false {
		t.Fatalf("images not an independent opt-in: %v", view)
	}
	const path = "/api/local/apps/observation"
	read := map[string]any{"app_id": "douyin", "connection_id": douyinapp.WebReadConnectionID, "content_id": douyinapp.WebWorksContentID, "field": "account_works", "refresh": true}
	list := func() (map[string]any, map[string]any) {
		t.Helper()
		code, result := f.request("POST", path, read, true)
		if code != 200 || strings.Contains(fmt.Sprint(result), "test-signature") {
			t.Fatalf("works: %d %v", code, result)
		}
		chunks := result["chunks"].([]any)
		return chunks[0].(map[string]any), chunks[1].(map[string]any)
	}
	public, _ := list()
	detail := map[string]any{"app_id": "douyin", "connection_id": douyinapp.WebReadConnectionID, "content_id": public["follow_up_content_id"], "field": "images"}
	if code, _ := f.request("POST", path, detail, true); code != 403 {
		t.Fatalf("image read before consent: %d", code)
	}
	permit("set_images", "allow_images", true, 200)
	public, private := list()
	if !strings.Contains(fmt.Sprint(public["follow_up_fields"]), "images") || strings.Contains(fmt.Sprint(private["follow_up_fields"]), "images") {
		t.Fatalf("wrong follow-ups: %v %v", public, private)
	}
	detail["content_id"] = private["follow_up_content_id"]
	if code, _ := f.request("POST", path, detail, true); code != 403 {
		t.Fatalf("private images exposed: %d", code)
	}
	detail["content_id"], detail["limit"] = public["follow_up_content_id"], 7
	if code, _ := f.request("POST", path, detail, true); code != 400 {
		t.Fatalf("invalid limit: %d", code)
	}
	delete(detail, "limit")
	code, first := f.request("POST", path, detail, true)
	if code != 200 || first["complete"] != false || !strings.Contains(fmt.Sprint(first["chunks"]), "图片位置 01") {
		t.Fatalf("first image batch: %d %v", code, first)
	}
	reordered = true
	if code, result := f.request("POST", path, detail, true); code != 409 || result["error"] != apphost.ErrObservationImagesSourceChanged.Error() {
		t.Fatalf("reordered images accepted: %d %v", code, result)
	}
	reordered = false
	code, next := f.request("POST", path, detail, true)
	if code != 200 || next["complete"] != true || !strings.Contains(fmt.Sprint(next["chunks"]), "图片位置 2") {
		t.Fatalf("image continuation: %d %v", code, next)
	}
	visibility = "仅自己可见"
	if code, _ := f.request("POST", path, detail, true); code != 409 {
		t.Fatalf("visibility change after completed observation accepted: %d", code)
	}
	visibility, owner = "公开", "different-account"
	if code, _ := f.request("POST", path, detail, true); code != 409 {
		t.Fatalf("account switch accepted: %d", code)
	}
	permit("set_works", "allow_works", false, 200)
	if code, _ := f.request("POST", path, detail, true); code != 403 || view["images_enabled"] != false {
		t.Fatalf("images not revoked with works: %d %v", code, view)
	}
	if downloads != 2 || visions != 2 {
		t.Fatalf("unexpected downstream calls: %d downloads, %d vision requests", downloads, visions)
	}
}
