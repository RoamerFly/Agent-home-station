package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"wechat-bot/internal/companion"
)

func TestWebMediaDownloadDouyinUsesStationOnceAndReusesVideo(t *testing.T) {
	f, access := trustedAgent(t)
	f.manager.native = &fakeTools{fakeNative: f.native}
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.Permissions.SendFile = true
	f.manager.mu.Unlock()

	const id = "7702768654487735586"
	video := []byte{0x1a, 0x45, 0xdf, 0xa3, 0x42, 0x86, 0x81, 0x01}
	hash := sha256.Sum256(video)
	var downloads atomic.Int32
	station := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session":
			http.SetCookie(w, &http.Cookie{Name: "station", Value: "fixture"})
			_, _ = w.Write([]byte(`{"authenticated":true,"csrf":"fixture"}`))
		case "/api/local/apps/douyin/media/" + id:
			if r.Header.Get("Origin") != "http://"+r.Host || r.Header.Get("Cookie") != "station=fixture" {
				t.Errorf("station session missing from media request")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			downloads.Add(1)
			w.Header().Set("Content-Type", "video/webm")
			w.Header().Set("Content-Length", strconv.Itoa(len(video)))
			w.Header().Set("X-Content-SHA256", hex.EncodeToString(hash[:]))
			_, _ = w.Write(video)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(station.Close)
	t.Setenv("AGENT_STATION_URL", station.URL)

	for _, raw := range []string{
		"https://www.douyin.com/video/" + id,
		"https://www.douyin.com/share/video/" + id,
	} {
		args, _ := json.Marshal(map[string]string{"url": raw, "kind": "file"})
		result, err := access.Call(context.Background(), "web_media_download", args)
		if err != nil {
			t.Fatalf("Douyin download %s: %v", raw, err)
		}
		var out struct {
			Media []companion.GeneratedMedia `json:"media"`
		}
		if json.Unmarshal([]byte(result), &out) != nil || len(out.Media) != 1 || out.Media[0].ID == "" || out.Media[0].Kind != "file" || out.Media[0].AssetID == "" || !strings.HasSuffix(out.Media[0].Title, ".webm") {
			t.Fatalf("missing staged media.id: %s", result)
		}
	}
	if got := downloads.Load(); got != 1 {
		t.Fatalf("same verified Douyin video downloaded %d times, want 1", got)
	}
}

func TestStationDouyinMediaRejectsBadDigestAndNonOwner(t *testing.T) {
	f, access := trustedAgent(t)
	f.manager.native = &fakeTools{fakeNative: f.native}
	const id = "7702768654487735586"
	video := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 1, 'i', 's', 'o', 'm', 'm', 'p', '4', '2'}
	station := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session":
			http.SetCookie(w, &http.Cookie{Name: "station", Value: "fixture"})
			_, _ = w.Write([]byte(`{"authenticated":true,"csrf":"fixture"}`))
		case "/api/local/apps/douyin/media/" + id:
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", strconv.Itoa(len(video)))
			w.Header().Set("X-Content-SHA256", strings.Repeat("0", 64))
			_, _ = w.Write(video)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(station.Close)
	t.Setenv("AGENT_STATION_URL", station.URL)
	if _, _, err := access.stationDouyinVideo(context.Background(), id); err == nil || err.Error() != "douyin_media_digest_mismatch" {
		t.Fatalf("corrupt Station media was accepted: %v", err)
	}
	access.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "wxid_owner", IsGroup: true, TraceID: "group-request"})
	if _, _, err := access.douyinVideoWithCache(context.Background(), "https://www.douyin.com/video/"+id, stationDouyinMediaLimit); err == nil || err.Error() != "douyin_owner_private_only" {
		t.Fatalf("group request used owner profile: %v", err)
	}
}
