//go:build windows

package station

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/stationbrowser"
)

func TestLocalDouyinCreatorVideoDetailsAndVisualUseCurrentWork(t *testing.T) {
	f, manager := newLocalDouyinWebFixture(t)
	view := enableSyntheticDouyinWebRead(t, f)
	view = setSyntheticDouyinWebScope(t, f, view, "home_and_videos")
	original := syntheticDouyinAudioMedia(t, false)
	const work = "7000000000000000011"
	downloads, analyses := 0, 0
	manager.readMedia = func(_ context.Context, _ string, id string, authorize func() error) (stationbrowser.DouyinWebMedia, error) {
		if id != work {
			t.Error("creator video changed before media read")
		}
		downloads++
		return stationbrowser.DouyinWebMedia{Data: original, ContentType: "video/mp4"}, authorize()
	}
	manager.visual = stationLocalVision{
		version: func(context.Context) (string, error) { return "synthetic-creator-vision", nil },
		observe: func(_ context.Context, _, _ string, frames []deepseek.Image, _ int, check func() error) (string, error) {
			analyses++
			if len(frames) != 2 || frames[0].MIME != "image/jpeg" || len(frames[0].Data) == 0 {
				t.Error("creator video did not reach frame analysis")
			}
			return "合成作品画面", check()
		},
	}
	code, view := enableSyntheticDouyinWebVisual(t, f, view, true)
	if code != 200 {
		t.Fatal(code, view)
	}
	if code, out := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_works", "revision": view["revision"], "allow_works": true}, true); code != 200 {
		t.Fatal(code, out)
	}
	visibility := "公开"
	manager.readWorks = func(_ context.Context, _ string, _ stationbrowser.DouyinCreatorWorksRequest, check func() error) (stationbrowser.DouyinCreatorWorksPage, error) {
		return stationbrowser.DouyinCreatorWorksPage{Account: "synthetic-creator", PageKey: "same-page", CoverageKnown: true,
			Works: []stationbrowser.DouyinCreatorWork{{ID: work, Title: "合成作品", Video: true, Visibility: visibility}}}, check()
	}
	const path = "/api/local/apps/observation"
	in := map[string]any{"app_id": "douyin", "connection_id": douyinapp.WebReadConnectionID, "content_id": douyinapp.WebWorksContentID, "field": "account_works"}
	code, list := f.request("POST", path, in, true)
	if code != 200 {
		t.Fatal(code, list)
	}
	in["content_id"], in["field"] = list["chunks"].([]any)[0].(map[string]any)["follow_up_content_id"], "page_text"
	code, details := f.request("POST", path, in, true)
	if code != 200 || details["chunks"].([]any)[0].(map[string]any)["follow_up_field"] != "visual" {
		t.Fatal("creator detail did not expose granted visual follow-up", code, details)
	}
	in["field"], in["limit"] = "visual", 2
	if code, out := f.request("POST", path, in, true); code != 200 || out["complete"] != false || out["visual_proof"] == nil || downloads != 1 || analyses != 1 {
		t.Fatal("creator video visual did not use the original source", code, out, downloads, analyses)
	}
	visibility = "仅自己可见"
	if code, out := f.request("POST", path, in, true); code != 409 || out["visual_proof"] != nil || downloads != 1 || analyses != 1 {
		t.Fatal("changed visibility reached another frame batch", code, out, downloads, analyses)
	}
}

func TestLocalDouyinWebVisualFollowsAuthorizedExactVideoAndReusesSource(t *testing.T) {
	f, manager := newLocalDouyinWebFixture(t)
	view := enableSyntheticDouyinWebRead(t, f)
	view = setSyntheticDouyinWebScope(t, f, view, "home_and_videos")
	target := syntheticDouyinWebPlaybackTarget(t, f, manager)
	in := syntheticDouyinWebPlaybackInput(target, 2)
	in["field"] = "visual"
	if code, _ := f.request("POST", "/api/local/apps/observation", in, true); code != http.StatusForbidden {
		t.Fatalf("visual without explicit grant: %d", code)
	}

	version, downloads, analyses := "synthetic-vision-v1", 0, 0
	var afterDecode func()
	original := syntheticDouyinAudioMedia(t, false)
	manager.readMedia = func(_ context.Context, _ string, videoID string, check func() error) (stationbrowser.DouyinWebMedia, error) {
		downloads++
		if videoID != "7000000000000000011" {
			t.Errorf("wrong original video: %s", videoID)
		}
		return stationbrowser.DouyinWebMedia{Data: original, ContentType: "video/mp4"}, check()
	}
	manager.visual = stationLocalVision{
		version: func(context.Context) (string, error) { return version, nil },
		observe: func(_ context.Context, source, prompt string, images []deepseek.Image, tokens int, check func() error) (string, error) {
			analyses++
			for _, frame := range images {
				if frame.MIME != "image/jpeg" || len(frame.Data) == 0 {
					t.Error("provider was not handed decoded images")
				}
			}
			if !strings.HasPrefix(source, "douyin-web-visual:") || !strings.Contains(prompt, "采样帧") || tokens != 0 {
				t.Errorf("invalid input to common vision: %s %s %d", source, prompt, tokens)
			}
			if err := check(); err != nil {
				return "", err
			}
			if afterDecode != nil {
				afterDecode()
			}
			return fmt.Sprintf("合成视觉观察，共 %d 帧。", len(images)), nil
		},
	}
	permit := func(revision any, allow bool) map[string]any {
		code, out := f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_read", "revision": revision, "allow_read": true, "read_scope": "home_and_videos", "allow_visual": allow}, true)
		if code != 200 || out["visual_enabled"] != allow || out["audio_enabled"] != false || out["playback_enabled"] != false {
			t.Fatalf("isolated visual grant: %d %v", code, out)
		}
		return out
	}
	view = permit(view["revision"], true)
	target = syntheticDouyinWebPlaybackTarget(t, f, manager)
	in["content_id"] = target
	in["limit"] = 13
	if code, _ := f.request("POST", "/api/local/apps/observation", in, true); code != http.StatusBadRequest || downloads != 0 {
		t.Fatalf("over-limit request got to the media read: %d / %d", code, downloads)
	}
	in["limit"] = 2
	code, first := f.request("POST", "/api/local/apps/observation", in, true)
	if code != 200 || first["complete"] != false || downloads != 1 || analyses != 1 {
		t.Fatalf("first page: %d %v %d/%d", code, first, downloads, analyses)
	}
	proof := first["visual_proof"].(map[string]any)
	indices, times := proof["frame_indexes"].([]any), proof["timestamp_seconds"].([]any)
	if len(indices) != 2 || indices[0] != float64(0) || indices[1] != float64(2) || times[1] != 0.4 {
		t.Fatalf("actual decoded positions: %v", proof)
	}
	chunk := first["chunks"].([]any)[0].(map[string]any)
	if chunk["source_url"] != "https://www.douyin.com/video/7000000000000000011" || chunk["source_id"] != proof["media_sha256"] || first["playback_proof"] != nil || first["audio_proof"] != nil {
		t.Fatalf("visual evidence conflated with another field: %v", first)
	}
	in["limit"] = 12
	code, last := f.request("POST", "/api/local/apps/observation", in, true)
	if code != 200 || last["complete"] != true || last["resumed"] != true || last["visual_proof"].(map[string]any)["start_frame"] != float64(4) || downloads != 1 || analyses != 2 {
		t.Fatalf("next page: %d %v %d/%d", code, last, downloads, analyses)
	}
	raw, _ := json.Marshal(last)
	if strings.Contains(string(raw), f.dir) || strings.Contains(string(raw), "data:image/") {
		t.Fatal("visual observation exposed image bytes or a host path")
	}
	code, cached := f.request("POST", "/api/local/apps/observation", in, true)
	if code != 200 || cached["cached"] != true || analyses != 2 {
		t.Fatalf("completed result repeated analysis: %d %v", code, cached)
	}
	version = "synthetic-vision-v2"
	code, changed := f.request("POST", "/api/local/apps/observation", in, true)
	if code != 200 || changed["cached"] != false || changed["visual_proof"].(map[string]any)["start_frame"] != float64(0) || downloads != 1 || analyses != 3 {
		t.Fatalf("changed model reused stale results or redownloaded source: %d %v", code, changed)
	}
	afterDecode = func() { view = permit(view["revision"], false) }
	in["refresh"] = true
	if code, result := f.request("POST", "/api/local/apps/observation", in, true); code != http.StatusForbidden || result["visual_proof"] != nil || analyses != 4 {
		t.Fatalf("late vision result after revocation: %d %v", code, result)
	}
	delete(in, "refresh")
	if code, _ := f.request("POST", "/api/local/apps/observation", in, true); code != http.StatusForbidden {
		t.Fatalf("revoked visual result returned: %d", code)
	}
}
