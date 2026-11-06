package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"
)

func TestLocalDouyinSearchAuthorityAndCredentialRedaction(t *testing.T) {
	base := t.TempDir()
	data, home := filepath.Join(base, "host"), filepath.Join(base, "workspace")
	settingsDir := filepath.Join(data, "apps", douyinapp.ID)
	registry, err := apphost.New(douyinapp.New(settingsDir).Registration())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewLocalHomeHandlerWithApps(data, home, "synthetic-local-password", "", registry)
	if err != nil {
		t.Fatal(err)
	}
	f := newLocalAppObservationFixtureWithHandler(t, handler, data, home)
	const connections = "/api/apps/douyin/connections"
	const observe = "/api/local/apps/observation"
	if code, _ := f.request(http.MethodGet, connections, nil, false); code != 401 {
		t.Fatalf("anonymous management read = %d", code)
	}
	f.login()
	if code, _ := f.request(http.MethodPost, "/api/local/home", map[string]string{"action": "initialize"}, true); code != 200 {
		t.Fatalf("initialize = %d", code)
	}
	if code, body := f.request(http.MethodGet, observe, nil, false); code != 200 || len(localAppObservationItems(body)) != 0 {
		t.Fatalf("unconfigured discovery = %d %v", code, body)
	}
	if err := os.MkdirAll(settingsDir, 0700); err != nil {
		t.Fatal(err)
	}
	const synthetic = "clt.syntheticfixturetokenusedonlyoffline"
	write := func(enabled bool, expires time.Time) {
		t.Helper()
		data, err := json.Marshal(map[string]any{
			"schema_version": 1, "enabled": enabled, "client_token": synthetic, "expires_at": expires.UTC().Format(time.RFC3339),
			"device_id": "1700000000000000000", "keywords": []string{"合成测试关键词"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(settingsDir, "search-settings.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(true, time.Now().Add(time.Hour))
	code, body := f.request(http.MethodGet, connections, nil, false)
	raw, _ := json.Marshal(body)
	if code != 200 || strings.Contains(string(raw), synthetic) || strings.Contains(string(raw), "1700000000000000000") {
		t.Fatalf("private application settings escaped connection view: %d %s", code, raw)
	}
	code, body = f.request(http.MethodGet, observe, nil, false)
	items := localAppObservationItems(body)
	if code != 200 || len(items) != 1 {
		t.Fatalf("authorized discovery = %d %v", code, body)
	}
	item := items[0].(map[string]any)
	if fields, ok := item["fields"].([]any); !ok || len(fields) != 1 || fields[0] != "search" || item["label"] != "合成测试关键词" {
		t.Fatalf("invalid discovery = %#v", item)
	}
	input := apphost.ObservationInput{AppID: douyinapp.ID, ConnectionID: "search", ContentID: item["content_id"].(string), Field: apphost.ObservationSearch}
	if code, _ := f.request(http.MethodPost, observe, input, false); code != 403 {
		t.Fatalf("search without CSRF = %d", code)
	}
	unapproved := input
	unapproved.ContentID = "unapproved-topic"
	if code, _ := f.request(http.MethodPost, observe, unapproved, true); code != 403 {
		t.Fatalf("search outside approval = %d", code)
	}
	write(true, time.Now().Add(-time.Hour))
	if code, _ := f.request(http.MethodPost, observe, input, true); code != 409 {
		t.Fatalf("expired search = %d", code)
	}
	write(false, time.Now().Add(time.Hour))
	if code, _ := f.request(http.MethodPost, observe, input, true); code != 403 {
		t.Fatalf("revoked search = %d", code)
	}
}

func TestLocalDouyinOwnVideoConnectionIsIndependent(t *testing.T) {
	base := t.TempDir()
	data, home := filepath.Join(base, "host"), filepath.Join(base, "workspace")
	settingsDir := filepath.Join(data, "apps", douyinapp.ID)
	registry, err := apphost.New(douyinapp.New(settingsDir).Registration())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewLocalHomeHandlerWithApps(data, home, "synthetic-local-password", "", registry)
	if err != nil {
		t.Fatal(err)
	}
	f := newLocalAppObservationFixtureWithHandler(t, handler, data, home)
	f.login()
	if code, _ := f.request(http.MethodPost, "/api/local/home", map[string]string{"action": "initialize"}, true); code != 200 {
		t.Fatalf("initialize = %d", code)
	}
	if err := os.MkdirAll(settingsDir, 0700); err != nil {
		t.Fatal(err)
	}
	const syntheticToken = "act.syntheticfixturetokenusedonlyoffline"
	const syntheticOpenID = "7e8df84c-6706-4df8-aedd-d10501c40402"
	const videoID = "7000000000000000001"
	path := filepath.Join(settingsDir, "own-videos-settings.json")
	write := func(expires time.Time) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"schema_version": 1, "enabled": true, "scope": "posting.behavior", "access_token": syntheticToken,
			"open_id": syntheticOpenID, "expires_at": expires.UTC().Format(time.RFC3339), "video_ids": []string{videoID},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(time.Now().Add(time.Hour))
	const connections = "/api/apps/douyin/connections"
	const observe = "/api/local/apps/observation"
	code, body := f.request(http.MethodGet, connections, nil, false)
	safe, _ := json.Marshal(body)
	if code != 200 || strings.Contains(string(safe), syntheticToken) || strings.Contains(string(safe), syntheticOpenID) {
		t.Fatalf("OAuth settings escaped the connection API: status=%d", code)
	}
	code, body = f.request(http.MethodGet, observe, nil, false)
	items := localAppObservationItems(body)
	if code != 200 || len(items) != 1 {
		t.Fatalf("own-video discovery = %d %v", code, body)
	}
	item := items[0].(map[string]any)
	fields, _ := item["fields"].([]any)
	if item["connection_id"] != "own-videos" || item["content_id"] != videoID || len(fields) != 1 || fields[0] != "own_video" {
		t.Fatalf("unexpected OAuth scope = %#v", item)
	}
	input := apphost.ObservationInput{AppID: douyinapp.ID, ConnectionID: "own-videos", ContentID: "7000000000000000002", Field: apphost.ObservationOwnVideo}
	if code, _ := f.request(http.MethodPost, observe, input, true); code != 403 {
		t.Fatalf("unapproved video = %d", code)
	}
	input.ContentID = videoID
	input.ConnectionID = "search"
	if code, _ := f.request(http.MethodPost, observe, input, true); code != 403 {
		t.Fatalf("search credential reused for account video = %d", code)
	}
	input.ConnectionID = "own-videos"
	write(time.Now().Add(-time.Hour))
	if code, _ := f.request(http.MethodPost, observe, input, true); code != 409 {
		t.Fatalf("expired OAuth grant = %d", code)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.request(http.MethodPost, observe, input, true); code != 403 {
		t.Fatalf("revoked OAuth grant = %d", code)
	}
}
