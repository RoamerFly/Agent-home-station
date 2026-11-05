package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
)

func TestLocalWechatLegacyImportKeepsEvidenceWithoutReplay(t *testing.T) {
	base := t.TempDir()
	host, work, snapshot := filepath.Join(base, "host"), filepath.Join(base, "workspace"), filepath.Join(base, "snapshot")
	legacyHost := filepath.Join(base, "old-host")
	app := filepath.Join(host, "apps", "wechat")
	native := filepath.Join(app, "native")
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	j := func(v any) []byte { b, _ := json.Marshal(v); return b }
	write(filepath.Join(native, "api-token"), []byte("synthetic-legacy-native-token"))
	sourceConfig := filepath.Join(app, legacyImportSourceFile)
	source := legacyImportSource{Root: filepath.ToSlash(snapshot), LegacyHostRoot: filepath.ToSlash(legacyHost), Primary: "one", Stopped: true}
	marker := legacyStopMarker{Version: 1, State: "stopped", Epoch: strings.Repeat("a", 32), AccountID: "one", DataRoot: legacyHost, NativeServerID: "old-dataset", At: time.Now().UTC().Add(-time.Minute)}
	liveMarker := filepath.Join(legacyHost, legacyStopMarkerFile)
	write(liveMarker, j(marker))
	write(filepath.Join(snapshot, legacyStopMarkerFile), j(marker))
	profile := filepath.Join(native, "accounts", "one", "profile")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(app, "native-import-"+wechatapp.ConnectionID("one")+".json")
	receiptProfile := profile
	if os.PathSeparator == '\\' {
		receiptProfile = `\\?\` + profile
	}
	receiptJSON := j(map[string]string{"id": "one", "storageMode": "managed", "profilePath": receiptProfile})
	write(receiptPath, receiptJSON)
	write(sourceConfig, j(source))
	const rawConfig = `{"enabled":true,"connection_paused":false,"api_key":"synthetic-do-not-copy-legacy-key","private_enabled":true,"private_all_friends":true,"direct_ids":["friend-one"],"filehelper_enabled":true,"group_rules":[{"id":"123@chatroom","mode":"mention"},{"id":"124@chatroom","mode":"all"}]}`
	configPath := filepath.Join(snapshot, "admin", "config.json")
	write(configPath, []byte(rawConfig))
	write(filepath.Join(snapshot, "admin", "accounts.json"), j([]accountRecord{{ID: "two", Removed: true}, {ID: "three"}, {ID: "four"}, {ID: "five"}}))
	write(filepath.Join(snapshot, "admin", "accounts", accountDirectory("three"), "config.json"), []byte(rawConfig))
	write(filepath.Join(snapshot, "admin", "accounts", accountDirectory("four"), "config.json"), []byte(rawConfig))
	write(filepath.Join(snapshot, "admin", "accounts", accountDirectory("five"), "config.json"), []byte(rawConfig))
	queuePath := filepath.Join(snapshot, "bot-state", "messages-v2.json")
	const rawQueue = `{"version":1,"server":"old-dataset","account":"one","inbox":[{"phase":"received"}],"deliveries":[{"key":"legacy-accepted","id":"job-accepted","thread":"filehelper","state":"accepted","server_id":"69123","text":"do-not-import-old-body","input":{"account":"one","thread":"filehelper"}},{"key":"legacy-pending","id":"job-pending","thread":"filehelper","state":"waiting","text":"do-not-import-old-draft","input":{"account":"one","thread":"filehelper"}}]}`
	write(queuePath, []byte(rawQueue))
	write(filepath.Join(snapshot, "bot-state", "checkpoint.json"), []byte(`{"version":1,"server_id":"old-dataset","account":"one","cursor":"62"}`))
	write(filepath.Join(snapshot, "bot-state", "accounts", accountDirectory("three"), "messages-v2.json"), []byte(strings.ReplaceAll(rawQueue, `"account":"one"`, `"account":"two"`)))
	sameQueue := strings.ReplaceAll(strings.ReplaceAll(rawQueue, `"account":"one"`, `"account":"four"`), "old-dataset", "new-dataset")
	write(filepath.Join(snapshot, "bot-state", "accounts", accountDirectory("four"), "messages-v2.json"), []byte(sameQueue))

	var sideEffects, receiptReads atomic.Int32
	var loginOnline atomic.Bool
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-legacy-native-token" {
			t.Error("wrong daemon credential")
			w.WriteHeader(401)
			return
		}
		if r.Method != http.MethodGet {
			sideEffects.Add(1)
			w.WriteHeader(400)
			return
		}
		switch r.URL.Path {
		case "/v1/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"pid": 9432, "dataDir": native, "serverId": "new-dataset", "ownershipGeneration": "synthetic-generation"})
		case "/v1/accounts":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]string{{"id": "one"}, {"id": "two"}, {"id": "three"}, {"id": "four"}, {"id": "five"}}})
		case "/v1/accounts/one", "/v1/accounts/four", "/v1/accounts/five":
			account := filepath.Base(r.URL.Path)
			connection := "offline"
			online := account == "one" && loginOnline.Load()
			if online {
				connection = "online"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": account, "storageMode": "managed", "authenticated": online, "connection": connection})
		case "/v1/accounts/four/sends/job-pending":
			receiptReads.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "job-pending", "accountId": "four", "threadId": "filehelper", "kind": "text", "state": "accepted", "serverId": "69234"})
		default:
			t.Error("import contacted an unexpected native endpoint", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer daemon.Close()
	entry, err := wechatapp.StationRegistration(t.Context(), daemon.URL, 9432, host)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := apphost.New(entry)
	if err != nil {
		t.Fatal(err)
	}
	open := func() (*LocalHomeHandler, *localHomeFixture) {
		t.Helper()
		handler, err := NewLocalHomeHandlerWithApps(host, work, "synthetic-local-password", "", registry)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		t.Cleanup(func() { _ = handler.Close() })
		jar, _ := cookiejar.New(nil)
		return handler, &localHomeFixture{t: t, server: server, client: &http.Client{Jar: jar}}
	}
	h, f := open()
	route := func(id string) string { return "/api/apps/wechat/connections/" + wechatapp.ConnectionID(id) }
	planRoute := func(id string) string { return route(id) + "/legacy-import" }
	req := func(method, path string, input any, csrf bool, want int) map[string]any {
		t.Helper()
		code, body := f.request(method, path, input, csrf)
		if code != want {
			t.Fatalf("%s %s: got %d want %d: %#v", method, path, code, want, body)
		}
		return body
	}
	req("GET", planRoute("one"), nil, false, 401)
	f.login()
	req("GET", planRoute("unknown"), nil, false, 404)
	req("GET", planRoute("two"), nil, false, 404)
	req("GET", planRoute("three"), nil, false, 422)
	detailFor := func(account string) map[string]any {
		return req("GET", route(account), nil, false, 200)["connection"].(map[string]any)
	}
	if detailFor("one")["legacy_import"] != "available" || detailFor("two")["legacy_import"] != nil {
		t.Fatal("legacy entry must be scoped to the staged account")
	}
	five := req("GET", planRoute("five"), nil, false, 200)
	req("PUT", route("five")+"/policy", map[string]any{"reply_threads": []string{"existing-target-thread"}}, true, 200)
	req("POST", planRoute("five"), planDigest(five), true, 409)
	req("GET", planRoute("five"), nil, false, 409)
	existing := detailFor("five")
	if existing["legacy_import"] != "conflict" || !bytes.Equal(j(existing["configured_policy"].(map[string]any)["reply_threads"]), []byte(`["existing-target-thread"]`)) {
		t.Fatal("import modified the existing target policy", existing)
	}
	write(sourceConfig, j(legacyImportSource{Root: host, Primary: "one", Stopped: true}))
	req("GET", planRoute("one"), nil, false, 422)
	if detailFor("one")["legacy_import"] != "invalid" {
		t.Fatal("invalid snapshot was advertised as available")
	}
	write(sourceConfig, j(source))
	req("POST", route("one")+"/lifecycle", map[string]string{"action": "start"}, true, 409)
	req("POST", route("one")+"/lifecycle", map[string]string{"action": "stop"}, true, 200)
	assertCutover := func(plan map[string]any, host, profile, login string) {
		t.Helper()
		want := legacyCutover{LegacyHost: host, NativeProfile: profile, TargetLogin: login, TargetIdentity: "owner_check_pending"}
		var got legacyCutover
		if err := json.Unmarshal(j(plan["cutover"]), &got); err != nil || got != want {
			t.Fatalf("cutover = %#v, want %#v (%v)", got, want, err)
		}
	}
	plan := req("GET", planRoute("one"), nil, false, 200)
	assertCutover(plan, "stop_record_matched", "import_receipt_matched", "signed_out")
	if strings.Contains(string(j(plan)), "old-host") || strings.Contains(string(j(plan)), marker.Epoch) {
		t.Fatal("cutover exposed private evidence")
	}
	starting := marker
	starting.State, starting.Epoch = "starting", strings.Repeat("b", 32)
	write(liveMarker, j(starting))
	req("POST", planRoute("one"), planDigest(plan), true, 409)
	assertCutover(req("GET", planRoute("one"), nil, false, 200), "changed", "import_receipt_matched", "signed_out")
	write(liveMarker, j(marker))
	write(receiptPath, j(map[string]string{"id": "four", "storageMode": "managed", "profilePath": profile}))
	req("POST", planRoute("one"), planDigest(plan), true, 409)
	assertCutover(req("GET", planRoute("one"), nil, false, 200), "stop_record_matched", "mismatch", "signed_out")
	write(receiptPath, receiptJSON)
	if plan["legacy_enabled"] != true || plan["same_native_dataset"] != false || plan["receipt_records"] != float64(2) || plan["unresolved_receipts"] != float64(1) ||
		!bytes.Equal(j(plan["suggested_reply_threads"]), []byte(`["123@chatroom","filehelper","friend-one"]`)) || strings.Contains(string(j(plan)), "synthetic-do-not-copy") {
		t.Fatal(plan)
	}
	if _, err := os.Stat(filepath.Join(app, "workers", wechatapp.ConnectionID("one"))); !os.IsNotExist(err) {
		t.Fatal("preview created worker data", err)
	}
	req("POST", planRoute("one"), planDigest(plan), false, 403)
	write(queuePath, []byte(rawQueue+"\n"))
	req("POST", planRoute("one"), planDigest(plan), true, 409)
	write(queuePath, []byte(rawQueue))
	applied := req("POST", planRoute("one"), planDigest(plan), true, 200)
	if applied["applied"] != true || req("POST", planRoute("one"), planDigest(plan), true, 200)["applied"] != true {
		t.Fatal(applied)
	}
	detail := req("GET", route("one"), nil, false, 200)["connection"].(map[string]any)
	if detail["legacy_import"] != "applied" || detail["worker"] != "paused" || detail["configured_policy"].(map[string]any)["conversation_enabled"] != false {
		t.Fatal(detail)
	}
	req("POST", route("one")+"/lifecycle", map[string]string{"action": "start"}, true, 409)
	accepted := req("GET", route("one")+"/receipts/legacy-accepted", nil, false, 200)
	if accepted["state"] != "accepted" || accepted["lookup"] != "legacy_snapshot" || accepted["server_id"] != "69123" {
		t.Fatal(accepted)
	}
	pending := req("GET", route("one")+"/receipts/legacy-pending", nil, false, 200)
	if pending["state"] != "submission_unknown" || pending["lookup"] != "legacy_snapshot" || receiptReads.Load() != 0 {
		t.Fatal(pending)
	}
	four := req("GET", planRoute("four"), nil, false, 200)
	if four["same_native_dataset"] != true {
		t.Fatal(four)
	}
	req("POST", planRoute("four"), planDigest(four), true, 200)
	if recovered := req("GET", route("four")+"/receipts/legacy-pending", nil, false, 200); recovered["lookup"] != "platform" || recovered["server_id"] != "69234" {
		t.Fatal(recovered)
	}
	req("GET", route("three")+"/receipts/legacy-accepted", nil, false, 404)
	target := filepath.Join(app, "workers", wechatapp.ConnectionID("one"))
	files, err := os.ReadDir(target)
	if err != nil || len(files) != 2 {
		t.Fatal("migration installed unexpected worker files", files, err)
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(target, file.Name()))
		if err != nil || bytes.Contains(data, []byte("do-not-import")) || bytes.Contains(data, []byte("do-not-copy")) {
			t.Fatal("migration copied raw source content", file.Name(), err)
		}
	}
	for path, expected := range map[string]string{configPath: rawConfig, queuePath: rawQueue} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != expected {
			t.Fatal("migration changed the snapshot", err)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	_, f = open()
	f.login()
	loginOnline.Store(true)
	write(liveMarker, j(starting))
	assertCutover(req("GET", planRoute("one"), nil, false, 200), "changed", "import_receipt_matched", "online")
	assertCutover(req("POST", planRoute("one"), planDigest(plan), true, 200), "stop_record_matched", "import_receipt_matched", "signed_out")
	if err := os.Remove(sourceConfig); err != nil {
		t.Fatal(err)
	}
	assertCutover(req("GET", planRoute("one"), nil, false, 200), "pending", "import_receipt_matched", "online")
	if detailFor("one")["legacy_import"] != "applied" || req("GET", planRoute("one"), nil, false, 200)["applied"] != true ||
		req("POST", planRoute("one"), planDigest(plan), true, 200)["applied"] != true {
		t.Fatal("saved migration report was not available after detaching the source")
	}
	if req("GET", route("one")+"/receipts/legacy-accepted", nil, false, 200)["state"] != "accepted" ||
		req("GET", route("one"), nil, false, 200)["connection"].(map[string]any)["worker"] != "paused" ||
		sideEffects.Load() != 0 || receiptReads.Load() != 1 {
		t.Fatal("migration was not durable or triggered native side effects")
	}
}

func planDigest(plan map[string]any) map[string]any { return map[string]any{"digest": plan["digest"]} }
