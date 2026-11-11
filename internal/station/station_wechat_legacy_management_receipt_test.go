package station

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
)

func TestLegacyManagementReceiptImportIsLocalAndSeparate(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the source snapshot is a Windows desktop artifact")
	}
	base := t.TempDir()
	legacy, snapshot := filepath.Join(base, "legacy"), filepath.Join(base, "snapshot")
	fixture := newLegacyWechatSnapshotFixture(t, legacy)
	mustWriteSnapshotFixture(t, filepath.Join(legacy, "admin", "tool-state.json"), []byte(`{
		"version":1,"sends":{
			"accepted-one":{"key":"accepted-one","hash":"synthetic","account":"one","thread":"manual-thread","kind":"text","status":{"id":"manual-job","state":"accepted","server_id":"72123"}},
			"management-only":{"key":"management-only","hash":"synthetic","account":"one","thread":"manual-thread","kind":"voice","status":{"id":"manual-voice-job","state":"running","voice_parts_incomplete":true,"voice_chunks":[{"index":0,"state":"accepted","server_id":"72124","has_receipt":true},{"index":1,"state":"prepared"}]}}
		},"uploads":{"private":"synthetic-not-a-receipt"}}`))
	capture := LegacyWechatSnapshotRequest{LegacyDataDir: legacy, SnapshotDir: snapshot, PrimaryAccount: fixture.primary}
	preview, err := CaptureLegacyWechatSnapshot(t.Context(), capture)
	if err != nil {
		t.Fatal(err)
	}
	capture.ApplyDigest = preview.ApplyDigest
	if _, err := CaptureLegacyWechatSnapshot(t.Context(), capture); err != nil {
		t.Fatal(err)
	}
	host, home := filepath.Join(base, "host"), filepath.Join(base, "home")
	app := filepath.Join(host, "apps", "wechat")
	native := filepath.Join(app, "native")
	mustWriteSnapshotFixture(t, filepath.Join(native, "api-token"), []byte("synthetic-legacy-native-token"))
	config, err := json.Marshal(legacyImportSource{Root: snapshot, LegacyHostRoot: legacy, Primary: fixture.primary, Stopped: true, ManifestDigest: preview.ManifestDigest})
	if err != nil {
		t.Fatal(err)
	}
	mustWriteSnapshotFixture(t, filepath.Join(app, legacyImportSourceFile), config)
	var nativeCalls atomic.Int32
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nativeCalls.Add(1)
		if r.Method != http.MethodGet {
			t.Error("receipt import attempted a native write")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var result any
		switch r.URL.Path {
		case "/v1/status":
			result = map[string]any{"pid": 9432, "dataDir": native, "serverId": "new-dataset", "ownershipGeneration": "synthetic-generation"}
		case "/v1/accounts":
			result = map[string]any{"items": []map[string]string{{"id": fixture.primary}, {"id": fixture.secondary}}}
		case "/v1/accounts/one", "/v1/accounts/two":
			result = map[string]any{"id": filepath.Base(r.URL.Path), "storageMode": "managed", "authenticated": false, "connection": "offline"}
		default:
			t.Errorf("receipt import attempted an unexpected native query %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	t.Cleanup(daemon.Close)
	entry, err := wechatapp.StationRegistration(t.Context(), daemon.URL, 9432, host)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := apphost.New(entry)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewLocalHomeHandlerWithApps(host, home, "synthetic-local-password", "", registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	jar, _ := cookiejar.New(nil)
	f := &localHomeFixture{t: t, server: server, client: &http.Client{Jar: jar}, dir: host, root: home, handler: handler}
	req := func(method, path string, data any, expected int) map[string]any {
		t.Helper()
		code, result := f.request(method, path, data, method == "POST")
		if code != expected {
			t.Fatalf("%s %s: %d want %d: %#v", method, path, code, expected, result)
		}
		return result
	}
	route := func(account string) string {
		return "/api/apps/wechat/connections/" + wechatapp.ConnectionID(account) + "/legacy-import"
	}
	primary, secondary := route(fixture.primary), route(fixture.secondary)
	const origin = "?origin=management"
	req("GET", primary+"/receipts"+origin, nil, 401)
	f.login()
	plan := req("GET", primary, nil, 200)
	if plan["receipt_records"] != float64(1) || plan["management_receipt_records"] != float64(2) || plan["management_unresolved_receipts"] != float64(1) {
		t.Fatal("preview omitted the separate ledger counts", plan)
	}
	projection := filepath.Join(snapshot, "admin", "tool-state.json")
	valid := mustReadSnapshotFixture(t, projection)
	mustWriteSnapshotFixture(t, projection, append(valid, '\n'))
	req("POST", primary, map[string]any{"digest": plan["digest"]}, 422)
	mustWriteSnapshotFixture(t, projection, valid)
	req("POST", primary, map[string]any{"digest": plan["digest"]}, 200)
	other := req("GET", secondary, nil, 200)
	if other["management_receipt_records"] != nil {
		t.Fatal("secondary account read the primary ledger", other)
	}
	req("POST", secondary, map[string]any{"digest": other["digest"]}, 200)
	beforeSurf := nativeCalls.Load()
	supplement := exerciseLegacySurfSupplement(t, f, legacy, snapshot, fixture, primary, secondary)
	if nativeCalls.Load() != beforeSurf {
		t.Fatal("surf archive contacted the native service")
	}
	activation := req("GET", primary+"/activation", nil, 200)
	if activation["management_pending_receipts"] != float64(1) {
		t.Fatal("activation omitted unresolved management receipts", activation)
	}
	if err := os.Rename(snapshot, filepath.Join(base, "detached-snapshot")); err != nil {
		t.Fatal(err)
	}
	before := nativeCalls.Load()
	page := req("GET", primary+"/receipts"+origin+"&limit=1", nil, 200)
	first := page["items"].([]any)[0].(map[string]any)
	if page["coverage"] != legacyManagementReceiptLookup || first["lookup"] != legacyManagementReceiptLookup || first["state"] != "accepted" || first["operation_id"] != "manual-job" {
		t.Fatal("management history lost its own receipt for the shared key", page)
	}
	worker := req("GET", primary+"/receipts", nil, 200)["items"].([]any)[0].(map[string]any)
	if worker["idempotency_key"] != first["idempotency_key"] || worker["operation_id"] != "job-one" {
		t.Fatal("management receipt overwrote the worker ledger", worker)
	}
	after := "after=" + url.QueryEscape(page["next_after"].(string))
	req("GET", primary+"/receipts?"+after, nil, 409)
	req("GET", primary+"/receipts"+origin+"&"+after, nil, 200)
	filtered := req("GET", primary+"/receipts"+origin+"&pending=1", nil, 200)
	detail := req("GET", primary+"/receipts/management-only"+origin, nil, 200)
	if len(filtered["items"].([]any)) != 1 || detail["state"] != "submitted_pending" || detail["lookup"] != legacyManagementReceiptLookup ||
		len(detail["accepted_parts"].([]any)) != 1 || len(detail["unconfirmed_parts"].([]any)) != 1 {
		t.Fatal("management snapshot lost the uncertainty or accepted part", detail)
	}
	req("GET", primary+"/receipts/management-only", nil, 404)
	req("GET", secondary+"/receipts/management-only"+origin, nil, 404)
	history := req("GET", primary+"/surf?part=history", nil, 200)
	if len(history["sessions"].([]any)) != 2 {
		t.Fatal("offline surf archive exposed a conversation row", history)
	}
	if err := os.Rename(supplement, filepath.Join(base, "detached-surf")); err != nil {
		t.Fatal(err)
	}
	req("GET", primary+"/surf", nil, 422)
	req("GET", primary+"/receipts?origin=invalid", nil, 400)
	if nativeCalls.Load() != before {
		t.Fatal("offline archive read contacted the native service")
	}
	req("GET", "/api/apps/wechat/connections/"+wechatapp.ConnectionID(fixture.primary)+"/receipts/management-only", nil, 404)

	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	beforeOffline := nativeCalls.Load()
	f = newLocalHomeFixture(t, host, home)
	req("GET", primary+"/receipts"+origin, nil, 401)
	req("GET", primary+"/surf", nil, 401)
	f.login()
	apps := req("GET", "/api/apps", nil, 200)["items"].([]any)
	if len(apps) != 1 || apps[0].(map[string]any)["registration"] != "station-archive" {
		t.Fatal("archive was lost with the native registration", apps)
	}
	rows := req("GET", "/api/apps/wechat/connections", nil, 200)["items"].([]any)
	if len(rows) != 2 {
		t.Fatal("previously saved accounts were not independently available", rows)
	}
	offline := req("GET", primary+"/receipts/management-only"+origin, nil, 200)
	if offline["lookup"] != legacyManagementReceiptLookup || offline["state"] != "submitted_pending" {
		t.Fatal("offline mode changed the archived evidence", offline)
	}
	req("GET", secondary+"/receipts/management-only"+origin, nil, 404)
	connection := "/api/apps/wechat/connections/" + wechatapp.ConnectionID(fixture.primary)
	req("POST", connection+"/lifecycle", map[string]string{"action": "start"}, http.StatusNotImplemented)
	if nativeCalls.Load() != beforeOffline || f.handler.chat.wechat != nil {
		t.Fatalf("offline receipt retrieval contacted or started an application worker: calls before=%d after=%d", beforeOffline, nativeCalls.Load())
	}
}

func exerciseLegacySurfSupplement(t *testing.T, f *localHomeFixture, legacy, original string, fixture legacyWechatSnapshotFixture, primary, secondary string) string {
	t.Helper()
	req := func(method, path string, expected int) map[string]any {
		t.Helper()
		code, data := f.request(method, path, nil, false)
		if code != expected {
			t.Fatalf("%s %s = %d want %d: %#v", method, path, code, expected, data)
		}
		return data
	}
	if result := req("GET", primary+"/surf", 200); result["settings_status"] != "missing" {
		t.Fatal("old snapshot should still work", result)
	}
	settings := []byte(`{"settings":{"enabled":true,"force_share":true,"interval_minutes":45,"max_sessions_per_day":3,"topics":["海洋故事"],"threads":["synthetic-target@chatroom"],"interaction_grants":[{"platform_account":"synthetic-unmigrated-account"}],"discovery":{"sources":[{"name":"测试来源","url":"https://synthetic.example/feed?token=synthetic-url-secret","enabled":true,"kind":"text","type":"rss"}],"weights":{"search":15},"cooldown_days":7,"max_sources":2,"max_depth":1}}}`)
	history := []byte(`{"version":1,"items":[{"item":{"id":"known","title":"合成收藏","url":"https://synthetic.example/story?private=synthetic-url-secret"},"bookmarked":true,"shared":{"synthetic-target@chatroom":"2026-01-01T12:00:00Z"}}],"sessions":[
		{"account":"one","thread":"host:surf:one","at":"2026-01-03T12:00:00Z","state":"bookmarked","selected":"known","interest":"合成兴趣","candidates":[{"id":"known","title":"合成收藏","url":"https://synthetic.example/story?private=synthetic-url-secret","summary":"synthetic-unmigrated-body"}],"observations":[{"url":"https://synthetic.example/story","state":"body_read","job_id":"synthetic-unmigrated-job","offset":1,"returned_characters":10,"total_characters":15}]},
		{"account":"one","thread":"allowed@chatroom","at":"2026-01-02T12:00:00Z","state":"shared","interest":"synthetic-hidden-thread-interest"},
		{"account":"one","thread":"host:surf:one","at":"2026-01-01T12:00:00Z","state":"browsing","candidates":[{"id":"known","url":"https://synthetic.example/story?private=synthetic-url-secret"}]},
		{"account":"two","thread":"host:surf:two","at":"2026-01-04T12:00:00Z","interest":"synthetic-other-account"},
		{"thread":"allowed@chatroom","at":"2026-01-04T12:00:00Z","state":"synthetic-unbound-row"}]}`)
	mustWriteSnapshotFixture(t, filepath.Join(legacy, "admin", legacySurfSettingsFile), settings)
	mustWriteSnapshotFixture(t, filepath.Join(legacy, "admin", legacySurfHistoryFile), history)
	secondaryDir := filepath.Join("admin", "accounts", wechatapp.AccountDirectory(fixture.secondary))
	mustWriteSnapshotFixture(t, filepath.Join(legacy, secondaryDir, legacySurfSettingsFile), []byte(`{"settings":{"interval_minutes":30,"max_sessions_per_day":2,"topics":["次账号话题"]}}`))
	snapshot := filepath.Join(filepath.Dir(original), "surf-supplement")
	capture := LegacyWechatSnapshotRequest{LegacyDataDir: legacy, SnapshotDir: snapshot, PrimaryAccount: fixture.primary}
	preview, err := CaptureLegacyWechatSnapshot(t.Context(), capture)
	if err != nil {
		t.Fatal(err)
	}
	capture.ApplyDigest = preview.ApplyDigest
	if _, err := CaptureLegacyWechatSnapshot(t.Context(), capture); err != nil {
		t.Fatal(err)
	}
	assertPrivate := func(raw []byte) {
		t.Helper()
		for _, token := range []string{"synthetic-url-secret", "synthetic-unmigrated-", "synthetic-other-account", "synthetic-unbound-row", "synthetic-hidden-thread-interest"} {
			if strings.Contains(string(raw), token) {
				t.Fatalf("surf projection retained %s", token)
			}
		}
	}
	primaryHistory := filepath.Join(snapshot, "admin", legacySurfHistoryFile)
	valid := mustReadSnapshotFixture(t, primaryHistory)
	assertPrivate(valid)
	assertPrivate(mustReadSnapshotFixture(t, filepath.Join(snapshot, "admin", legacySurfSettingsFile)))
	source, err := json.Marshal(legacyImportSource{Root: snapshot, LegacyHostRoot: legacy, Primary: fixture.primary, Stopped: true, ManifestDigest: preview.ManifestDigest})
	if err != nil {
		t.Fatal(err)
	}
	mustWriteSnapshotFixture(t, filepath.Join(f.dir, "apps", "wechat", legacySurfSourceFile), source)
	got := req("GET", primary+"/surf", 200)
	configuration := got["settings"].(map[string]any)
	if got["schedule"] != "paused" || got["history_available"] != true || configuration["enabled"] != true || configuration["interval_max_minutes"] != float64(90) {
		t.Fatal("old settings were activated or lost", got)
	}
	discovery := configuration["discovery"].(map[string]any)
	if discovery["sources"].([]any)[0].(map[string]any)["site"] != "synthetic.example" {
		t.Fatal("configured source missing", discovery)
	}
	other := req("GET", secondary+"/surf", 200)
	if other["history_available"] != false || other["settings"].(map[string]any)["topics"].([]any)[0] != "次账号话题" {
		t.Fatal("secondary account scope mismatch", other)
	}
	req("GET", secondary+"/surf?part=history", 404)
	req("GET", primary+"/surf?limit=1", 400)
	response, err := f.client.Post(f.server.URL+primary+"/surf", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected surf write status: %d", response.StatusCode)
	}
	page := req("GET", primary+"/surf?part=history&limit=1", 200)
	first := page["sessions"].([]any)[0].(map[string]any)
	if first["thread"] != "" || first["candidates"].([]any)[0].(map[string]any)["bookmarked"] != true ||
		first["observations"].([]any)[0].(map[string]any)["returned_characters"] != float64(10) {
		t.Fatal("history lost scoped bookmark or evidence", page)
	}
	after := "&after=" + url.QueryEscape(page["next_after"].(string))
	next := req("GET", primary+"/surf?part=history"+after, 200)
	if len(next["sessions"].([]any)) != 1 || next["sessions"].([]any)[0].(map[string]any)["thread"] != "" ||
		next["sessions"].([]any)[0].(map[string]any)["candidates"].([]any)[0].(map[string]any)["bookmarked"] != nil {
		t.Fatal("conversation without history grant was returned", next)
	}
	worker, err := f.handler.chat.wechat.worker(wechatapp.ConnectionID(fixture.primary), false)
	if err != nil || worker == nil {
		t.Fatalf("synthetic worker unavailable: %v", err)
	}
	worker.mu.Lock()
	worker.policy.HistoryGrants = map[string]time.Time{"allowed@chatroom": time.Now()}
	worker.mu.Unlock()
	req("GET", primary+"/surf?part=history"+after, 409)
	granted := req("GET", primary+"/surf?part=history&limit=2", 200)
	if granted["sessions"].([]any)[1].(map[string]any)["thread"] != "allowed@chatroom" {
		t.Fatal("current history authorization not applied", granted)
	}
	worker.mu.Lock()
	worker.policy.HistoryGrants = nil
	worker.mu.Unlock()
	req("GET", primary+"/surf?part=history&after="+url.QueryEscape(granted["next_after"].(string)), 409)
	mustWriteSnapshotFixture(t, primaryHistory, append(append([]byte{}, valid...), '\n'))
	req("GET", primary+"/surf?part=history", 422)
	req("GET", primary+"/surf", 200)
	mustWriteSnapshotFixture(t, primaryHistory, valid)
	if string(mustReadSnapshotFixture(t, filepath.Join(legacy, "admin", legacySurfHistoryFile))) != string(history) {
		t.Fatal("old surf history was modified")
	}
	return snapshot
}
