package admin

import (
	"net/http"
	"path/filepath"
	"testing"

	biliapp "wechat-bot/connected-apps/bilibili/adapter"
	"wechat-bot/internal/apphost"
)

func TestLocalBilibiliOwnerAuthorizationAndRestart(t *testing.T) {
	base := t.TempDir()
	data, home := filepath.Join(base, "host"), filepath.Join(base, "workspace")
	newStation := func() *localHomeFixture {
		t.Helper()
		registry, err := apphost.New(biliapp.New(filepath.Join(data, "apps", biliapp.ID)).Registration())
		if err != nil {
			t.Fatal(err)
		}
		handler, err := NewLocalHomeHandlerWithApps(data, home, "synthetic-local-password", "", registry)
		if err != nil {
			t.Fatal(err)
		}
		return newLocalAppObservationFixtureWithHandler(t, handler, data, home)
	}
	const policy = "/api/local/apps/bilibili/public"
	const observe = "/api/local/apps/observation"
	const video = "BV1aaaaaaaaa"
	f := newStation()
	if code, _ := f.request(http.MethodGet, policy, nil, false); code != 401 {
		t.Fatalf("unprotected settings: %d", code)
	}
	f.login()
	if code, _ := f.request(http.MethodPost, "/api/local/home", map[string]string{"action": "initialize"}, true); code != 200 {
		t.Fatalf("initialize local owner: %d", code)
	}
	code, defaults := f.request(http.MethodGet, policy, nil, false)
	if code != 200 || defaults["enabled"] != false || defaults["browser_enabled"] != false || defaults["playback_enabled"] != false || defaults["comments_enabled"] != false {
		t.Fatalf("new connection was already authorized: %d %#v", code, defaults)
	}
	if _, ok := defaults["browser_available"].(bool); !ok {
		t.Fatalf("browser availability was missing: %#v", defaults)
	}
	settings := map[string]any{"revision": defaults["revision"], "enabled": true, "videos": []string{video}}
	unscopedComments := map[string]any{"revision": defaults["revision"], "enabled": false, "videos": []string{video}, "comments_enabled": true}
	if code, _ := f.request(http.MethodPut, policy, unscopedComments, true); code != 400 {
		t.Fatalf("comments enabled without base authorization: %d", code)
	}
	unscopedPlayback := map[string]any{"revision": defaults["revision"], "enabled": true, "videos": []string{video}, "playback_enabled": true}
	if code, _ := f.request(http.MethodPut, policy, unscopedPlayback, true); code != 400 {
		t.Fatalf("playback enabled without browser authorization: %d", code)
	}
	if code, _ := f.request(http.MethodPut, policy, settings, false); code != 403 {
		t.Fatalf("authorization changed without CSRF: %d", code)
	}
	code, saved := f.request(http.MethodPut, policy, settings, true)
	if code != 200 || saved["enabled"] != true {
		t.Fatalf("grant unavailable: %d %#v", code, saved)
	}
	if code, _ := f.request(http.MethodPut, policy, settings, true); code != 409 {
		t.Fatalf("stale revision applied: %d", code)
	}
	f.server.Close()
	if err := f.handler.Close(); err != nil {
		t.Fatal(err)
	}
	f = newStation()
	f.login()
	code, discovered := f.request(http.MethodGet, observe, nil, false)
	items := localAppObservationItems(discovered)
	if code != 200 || len(items) != 1 {
		t.Fatalf("grant did not survive restart: %d %#v", code, discovered)
	}
	item := items[0].(map[string]any)
	fields := item["fields"].([]any)
	if item["app_id"] != biliapp.ID || item["connection_id"] != "public" || item["account_id"] != "anonymous" || item["content_id"] != video ||
		len(fields) != 2 || fields[0] != "title" || fields[1] != "body" {
		t.Fatalf("unverified capability exposed: %#v", item)
	}
	for _, input := range []apphost.ObservationInput{
		{AppID: biliapp.ID, ConnectionID: "other", ContentID: video, Field: apphost.ObservationTitle},
		{AppID: biliapp.ID, ConnectionID: "public", ContentID: "BV1bbbbbbbbb", Field: apphost.ObservationTitle},
		{AppID: biliapp.ID, ConnectionID: "public", ContentID: video, Field: apphost.ObservationPlayback},
		{AppID: biliapp.ID, ConnectionID: "public", ContentID: video, Field: apphost.ObservationPageText},
		{AppID: biliapp.ID, ConnectionID: "public", ContentID: video, Field: apphost.ObservationComments},
	} {
		if code, _ := f.request(http.MethodPost, observe, input, true); code != 403 && code != 404 {
			t.Fatalf("unauthorized scope reached provider: %d %+v", code, input)
		}
	}
	settings["revision"], settings["comments_enabled"] = saved["revision"], true
	code, saved = f.request(http.MethodPut, policy, settings, true)
	if code != 200 || saved["comments_enabled"] != true || saved["browser_enabled"] != false {
		t.Fatalf("anonymous comments could not be enabled independently: %d %#v", code, saved)
	}
	code, discovered = f.request(http.MethodGet, observe, nil, false)
	items = localAppObservationItems(discovered)
	if code != 200 || len(items) != 1 || len(items[0].(map[string]any)["fields"].([]any)) != 3 ||
		items[0].(map[string]any)["fields"].([]any)[2] != "comments" {
		t.Fatalf("comments did not become discoverable: %d %#v", code, discovered)
	}
	settings["revision"], settings["enabled"], settings["comments_enabled"] = saved["revision"], false, false
	if code, _ := f.request(http.MethodPut, policy, settings, true); code != 200 {
		t.Fatalf("revoke: %d", code)
	}
	if code, result := f.request(http.MethodGet, observe, nil, false); code != 200 || len(localAppObservationItems(result)) != 0 {
		t.Fatalf("revoked content still discoverable: %d %#v", code, result)
	}
}
