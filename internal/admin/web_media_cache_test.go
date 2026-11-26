package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"wechat-bot/internal/companion"
)

func TestWebMediaDownloadReusesVerifiedURLWithoutNetwork(t *testing.T) {
	f, access := trustedAgent(t)
	f.manager.native = &fakeTools{fakeNative: f.native}
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.Permissions.SendFile = true
	f.manager.mu.Unlock()

	// A DNS name that cannot resolve proves a successful call used only the
	// previously verified bytes. The query is intentionally absent from the
	// persisted source_url and must still distinguish exact requests.
	raw := "https://must-not-resolve.invalid/photo.png?token=first"
	png := []byte("\x89PNG\r\n\x1a\nsynthetic-content")
	a, err := f.manager.rememberAssetFormats(access.trace, assetOriginal{Data: png, MIME: "image/png", URL: raw, RequestURL: raw, Filename: "photo.png"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.URL, "token=") || len(a.RequestURLHashes) != 1 {
		t.Fatalf("raw query leaked or exact URL missing: %+v", a)
	}
	for _, kind := range []string{"image", "file"} {
		args, _ := json.Marshal(map[string]string{"url": raw, "kind": kind})
		result, callErr := access.Call(context.Background(), "web_media_download", args)
		if callErr != nil {
			t.Fatalf("same URL kind=%s contacted network or failed: %v", kind, callErr)
		}
		var output struct {
			Media []companion.GeneratedMedia `json:"media"`
		}
		if json.Unmarshal([]byte(result), &output) != nil || len(output.Media) != 1 || output.Media[0].Kind != kind || output.Media[0].AssetID != a.ID {
			t.Fatalf("kind=%s did not prepare matching cached asset: %s", kind, result)
		}
	}
	if _, _, ok := f.manager.cachedWebMedia(access.trace, "https://must-not-resolve.invalid/photo.png?token=second", "file", false, toolsLimit("file")); ok {
		t.Fatal("different signed URL reused stripped source_url")
	}
	other := access.trace
	other.Thread = "other-conversation"
	if _, _, ok := f.manager.cachedWebMedia(other, raw, "file", false, toolsLimit("file")); ok {
		t.Fatal("cache crossed conversation scope")
	}
}

func TestWebMediaDownloadReusesPublicPageOriginalWithoutNetwork(t *testing.T) {
	f, access := trustedAgent(t)
	f.manager.native = &fakeTools{fakeNative: f.native}
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.Permissions.SendFile = true
	f.manager.mu.Unlock()
	page := "https://www.bilibili.com/video/BV1qP4y187Lf/?p=2"
	video := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 1, 'i', 's', 'o', 'm', 'm', 'p', '4', '2'}
	a, err := f.manager.rememberAssetFormats(access.trace, assetOriginal{Data: video, MIME: "video/mp4", URL: page, RequestURL: page, Filename: "cached-song.mp4"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"url": page, "kind": "file", "audio_only": false})
	result, err := access.Call(context.Background(), "web_media_download", args)
	if err != nil {
		t.Fatalf("cached public page contacted network or failed: %v", err)
	}
	var output struct {
		Media []companion.GeneratedMedia `json:"media"`
	}
	if json.Unmarshal([]byte(result), &output) != nil || len(output.Media) != 1 || output.Media[0].Kind != "file" || output.Media[0].AssetID != a.ID {
		t.Fatalf("public page did not reuse original video: %s", result)
	}
}
