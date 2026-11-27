package station

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"testing"

	"wechat-bot/internal/stationbrowser"
)

func douyinMediaRequest(t *testing.T, f *localHomeFixture, id string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.server.URL+"/api/local/apps/douyin/media/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", f.server.URL)
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, res.Header, body
}

func TestLocalDouyinMediaRouteRequiresOwnerAndCurrentReadGrant(t *testing.T) {
	f, m := newLocalDouyinWebFixture(t)
	const videoID = "7702768654487735586"
	video := []byte("\x00\x00\x00\x14ftypisomsynthetic-video")
	calls := 0
	m.readMedia = func(_ context.Context, profile, id string, check func() error) (stationbrowser.DouyinWebMedia, error) {
		calls++
		if profile == "" || id != videoID || check() != nil {
			t.Errorf("unbound video read: profile=%q id=%q", profile, id)
		}
		return stationbrowser.DouyinWebMedia{Data: video, ContentType: "video/mp4"}, nil
	}
	if status, _, _ := douyinMediaRequest(t, f, videoID); status != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("unauthenticated media request: status=%d calls=%d", status, calls)
	}
	initializeDouyinWeb(t, f)
	if status, _, _ := douyinMediaRequest(t, f, videoID); status != http.StatusForbidden || calls != 0 {
		t.Fatalf("unconfigured media request: status=%d calls=%d", status, calls)
	}
	viewCode, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "begin", "revision": 0, "approve_profile": true}, true)
	if viewCode != http.StatusOK {
		t.Fatal(viewCode, view)
	}
	viewCode, view = f.request("POST", localDouyinWebRoute, map[string]any{"action": "confirm", "revision": view["revision"], "attempt_id": view["attempt_id"]}, true)
	if viewCode != http.StatusOK {
		t.Fatal(viewCode, view)
	}
	if status, _, _ := douyinMediaRequest(t, f, "bad-id"); status != http.StatusBadRequest || calls != 0 {
		t.Fatalf("invalid ID request: status=%d calls=%d", status, calls)
	}
	status, headers, body := douyinMediaRequest(t, f, videoID)
	sum := sha256.Sum256(video)
	if status != http.StatusOK || calls != 1 || !bytes.Equal(body, video) || headers.Get("Content-Type") != "video/mp4" ||
		headers.Get("Content-Length") != "27" || headers.Get("X-Content-SHA256") != hex.EncodeToString(sum[:]) || headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("verified media response: status=%d calls=%d headers=%v body=%q", status, calls, headers, body)
	}
	viewCode, view = f.request("POST", localDouyinWebRoute, map[string]any{"action": "set_read", "revision": view["revision"], "allow_read": true, "read_scope": "home"}, true)
	if viewCode != http.StatusOK {
		t.Fatal(viewCode, view)
	}
	if status, _, _ := douyinMediaRequest(t, f, videoID); status != http.StatusForbidden || calls != 1 {
		t.Fatalf("narrowed scope was ignored: status=%d calls=%d", status, calls)
	}
}

func TestLocalDouyinMediaRouteStopsAfterGrantRevocation(t *testing.T) {
	f, m := newLocalDouyinWebFixture(t)
	initializeDouyinWeb(t, f)
	_, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "begin", "revision": 0, "approve_profile": true}, true)
	_, view = f.request("POST", localDouyinWebRoute, map[string]any{"action": "confirm", "revision": view["revision"], "attempt_id": view["attempt_id"]}, true)
	revision := view["revision"]
	pauseStatus := 0
	m.readMedia = func(_ context.Context, _ string, _ string, check func() error) (stationbrowser.DouyinWebMedia, error) {
		if err := check(); err != nil {
			t.Errorf("read began without grant: %v", err)
		}
		pauseStatus, _ = f.request("POST", localDouyinWebRoute, map[string]any{"action": "pause", "revision": revision}, true)
		return stationbrowser.DouyinWebMedia{Data: []byte("\x00\x00\x00\x14ftypisomsynthetic-video"), ContentType: "video/mp4"}, nil
	}
	status, headers, body := douyinMediaRequest(t, f, "7702768654487735586")
	if pauseStatus != http.StatusOK || status != http.StatusForbidden || headers.Get("X-Content-SHA256") != "" || !bytes.Contains(body, []byte(`"error"`)) {
		t.Fatalf("in-flight revocation: pause=%d response=%d headers=%v body=%s", pauseStatus, status, headers, body)
	}
}

func TestLocalDouyinMediaRouteRejectsNonVideoBody(t *testing.T) {
	f, m := newLocalDouyinWebFixture(t)
	initializeDouyinWeb(t, f)
	_, view := f.request("POST", localDouyinWebRoute, map[string]any{"action": "begin", "revision": 0, "approve_profile": true}, true)
	_, view = f.request("POST", localDouyinWebRoute, map[string]any{"action": "confirm", "revision": view["revision"], "attempt_id": view["attempt_id"]}, true)
	m.readMedia = func(_ context.Context, _ string, _ string, _ func() error) (stationbrowser.DouyinWebMedia, error) {
		return stationbrowser.DouyinWebMedia{Data: []byte("<html>challenge</html>"), ContentType: "video/mp4"}, nil
	}
	status, headers, _ := douyinMediaRequest(t, f, "7702768654487735586")
	if status != http.StatusBadGateway || headers.Get("X-Content-SHA256") != "" {
		t.Fatalf("HTML challenge exposed as video: status=%d headers=%v", status, headers)
	}
}
