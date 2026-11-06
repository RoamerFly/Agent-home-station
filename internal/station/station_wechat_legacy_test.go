package station

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

func TestLocalWechatLegacyImportKeepsEvidenceWithoutReplay(t *testing.T) {
	base := t.TempDir()
	host, work, snapshot := filepath.Join(base, "host"), filepath.Join(base, "workspace"), filepath.Join(base, "snapshot")
	// The WeChat cutover runs on the same station that already imported its
	// model, stable identity, persistent project and both private voice types.
	ownerIdentity, speakingVoice := prepareLegacyCutover(t, base, host, work)
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
	const rawConfig = `{"enabled":true,"connection_paused":false,"api_key":"synthetic-do-not-copy-legacy-key","private_enabled":true,"private_all_friends":true,"direct_ids":["friend-one"],"filehelper_enabled":true,"companion":{"enabled":true,"permissions":{"reply_text":true}},"group_rules":[{"id":"123@chatroom","mode":"mention"},{"id":"124@chatroom","mode":"all","prefix":"/group ","persona":"synthetic scoped role","force_reply_when_mentioned":true},{"id":"125@chatroom","mode":"social"}]}`
	configPath := filepath.Join(snapshot, "admin", "config.json")
	write(configPath, []byte(rawConfig))
	write(filepath.Join(snapshot, "admin", "accounts.json"), j([]wechatapp.LegacyAccountRecord{{ID: "two", Removed: true}, {ID: "three"}, {ID: "four"}, {ID: "five"}}))
	write(filepath.Join(snapshot, "admin", "accounts", wechatapp.AccountDirectory("three"), "config.json"), []byte(rawConfig))
	write(filepath.Join(snapshot, "admin", "accounts", wechatapp.AccountDirectory("four"), "config.json"), []byte(rawConfig))
	write(filepath.Join(snapshot, "admin", "accounts", wechatapp.AccountDirectory("five"), "config.json"), []byte(rawConfig))
	queuePath := filepath.Join(snapshot, "bot-state", "messages-v2.json")
	const rawQueue = `{"version":1,"server":"old-dataset","account":"one","inbox":[{"phase":"received"}],"deliveries":[{"key":"legacy-accepted","id":"job-accepted","thread":"filehelper","state":"accepted","server_id":"69123","text":"do-not-import-old-body","input":{"account":"one","thread":"filehelper"}},{"key":"legacy-operation-unknown","id":"","thread":"filehelper","state":"waiting","text":"do-not-import-old-unknown","input":{"account":"one","thread":"filehelper"}},{"key":"legacy-pending","id":"job-pending","thread":"filehelper","state":"waiting","text":"do-not-import-old-draft","input":{"account":"one","thread":"filehelper"}}]}`
	write(queuePath, []byte(rawQueue))
	write(filepath.Join(snapshot, "bot-state", "checkpoint.json"), []byte(`{"version":1,"server_id":"old-dataset","account":"one","cursor":"62"}`))
	write(filepath.Join(snapshot, "bot-state", "accounts", wechatapp.AccountDirectory("three"), "messages-v2.json"), []byte(strings.ReplaceAll(rawQueue, `"account":"one"`, `"account":"two"`)))
	sameQueue := strings.ReplaceAll(strings.ReplaceAll(rawQueue, `"account":"one"`, `"account":"four"`), "old-dataset", "new-dataset")
	write(filepath.Join(snapshot, "bot-state", "accounts", wechatapp.AccountDirectory("four"), "messages-v2.json"), []byte(sameQueue))

	var sideEffects, receiptReads atomic.Int32
	var daemonOffline atomic.Bool
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
		if daemonOffline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
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
		return handler, &localHomeFixture{t: t, server: server, client: &http.Client{Jar: jar}, dir: host, root: work, handler: handler}
	}
	h, f := open()
	route := func(id string) string { return "/api/apps/wechat/connections/" + wechatapp.ConnectionID(id) }
	planRoute := func(id string) string { return route(id) + "/legacy-import" }
	legacyReceiptRoute := func(id string) string { return planRoute(id) + "/receipts" }
	req := func(method, path string, input any, csrf bool, want int) map[string]any {
		t.Helper()
		code, body := f.request(method, path, input, csrf)
		if code != want {
			t.Fatalf("%s %s: got %d want %d: %#v", method, path, code, want, body)
		}
		return body
	}
	memoryRoute := func(account string) string { return planRoute(account) + "/memory" }
	ruleRoute := func(account, thread string) string { return planRoute(account) + "/groups/" + thread + "/rule" }
	req("GET", planRoute("one"), nil, false, 401)
	req("GET", legacyReceiptRoute("one"), nil, false, 401)
	req("GET", memoryRoute("one"), nil, false, 401)
	req("GET", memoryRoute("one")+"/123@chatroom/preference", nil, false, 401)
	req("GET", ruleRoute("one", "124@chatroom"), nil, false, 401)
	f.login()
	assertCutoverOwnerState(t, f, ownerIdentity, speakingVoice)
	req("GET", memoryRoute("two"), nil, false, 404)
	req("GET", memoryRoute("unknown"), nil, false, 404)
	if report := req("GET", memoryRoute("three"), nil, false, 200); report["status"] != "missing" {
		t.Fatal("secondary account fell back to primary memory", report)
	}
	legacyMemoryDir := filepath.Join(snapshot, "admin", "companion")
	write(filepath.Join(legacyMemoryDir, "memory.json"), []byte("{}"))
	if report := req("GET", memoryRoute("one"), nil, false, 200); report["status"] != "unsupported" || report["backend"] != "legacy_json" {
		t.Fatal("legacy JSON treated as a completed audit", report)
	}
	memory, err := companion.NewStore(legacyMemoryDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := companion.Defaults()
	cfg.Permissions.Remember = true
	now := time.Now().UTC()
	for _, in := range []companion.Input{
		{Channel: "wechat", Account: "one", Thread: "123@chatroom", TraceID: "chat", Sender: "synthetic-sender", Text: "synthetic-private-body", Source: &companion.SourceRef{Integrity: "observed", MessageType: 1, ServerID: "synthetic-native-original"}},
		{Channel: "wechat", Account: "one", Thread: "filehelper", TraceID: "helper", Sender: "synthetic-sender", Text: "synthetic-private-body", Source: &companion.SourceRef{Integrity: "observed", MessageType: 1, ServerID: "synthetic-native-original"}},
		{Channel: "wechat", Account: "other", Thread: "foreign-private-thread", TraceID: "foreign", Sender: "synthetic-sender", Text: "synthetic-private-body", Source: &companion.SourceRef{Integrity: "observed", MessageType: 1, ServerID: "synthetic-native-original"}},
	} {
		in.MessageTime = now.Format(time.RFC3339Nano)
		if err := memory.Observe(in, cfg, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := memory.Clear(companion.ScopeID("wechat", "one", "filehelper")); err != nil {
		t.Fatal(err)
	}
	const oldGroupNote = "合成群偏好：收到分享时默认总结。"
	if err := memory.SetPreferences(companion.ScopeID("wechat", "one", "123@chatroom"), oldGroupNote, 0); err != nil {
		t.Fatal(err)
	}
	if err := memory.Close(); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(legacyMemoryDir, "memory.sqlite")
	dbBefore, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// A mistakenly staged primary database cannot expose its scope IDs under a secondary account.
	secondaryMemory := filepath.Join(snapshot, "admin", "accounts", wechatapp.AccountDirectory("three"), "companion", "memory.sqlite")
	write(secondaryMemory, dbBefore)
	if report := req("GET", memoryRoute("three"), nil, false, 200); report["status"] != "ready" ||
		report["matched_scopes"] != float64(0) || report["foreign_scopes"] != float64(3) || len(report["conversations"].([]any)) != 0 {
		t.Fatal("wrong-account snapshot disclosed conversations", report)
	}
	write(dbPath+"-wal", []byte("synthetic WAL not a standalone snapshot"))
	if report := req("GET", memoryRoute("one"), nil, false, 200); report["status"] != "snapshot_incomplete" {
		t.Fatal("WAL-bearing snapshot treated as standalone", report)
	}
	if err := os.Remove(dbPath + "-wal"); err != nil {
		t.Fatal(err)
	}
	req("GET", memoryRoute("one")+"?limit=201", nil, false, 400)
	req("GET", memoryRoute("one")+"?after=bad", nil, false, 400)
	audit := req("GET", memoryRoute("one")+"?limit=1", nil, false, 200)
	if audit["status"] != "ready" || audit["backend"] != "sqlite" || audit["matched_scopes"] != float64(2) || audit["foreign_scopes"] != float64(1) {
		t.Fatal(audit)
	}
	first := audit["conversations"].([]any)[0].(map[string]any)
	if first["thread_id"] != "123@chatroom" || first["event_count"] != float64(1) || first["history_granted"] != false || first["has_preference"] != true {
		t.Fatal(first)
	}
	for _, private := range []string{"synthetic-private-body", "synthetic-sender", "foreign-private-thread", "memory.sqlite", oldGroupNote} {
		if strings.Contains(string(j(audit)), private) {
			t.Fatal("memory preview exposed private data", private)
		}
	}
	cursor := url.QueryEscape(audit["next_after"].(string))
	second := req("GET", memoryRoute("one")+"?after="+cursor, nil, false, 200)["conversations"].([]any)[0].(map[string]any)
	if second["thread_id"] != "filehelper" || second["event_count"] != float64(0) || second["withheld_events"] != float64(1) {
		t.Fatal(second)
	}
	if data, err := os.ReadFile(dbPath); err != nil || !bytes.Equal(dbBefore, data) {
		t.Fatal("read-only preview changed the source database", err)
	}
	for _, sidecar := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(dbPath + sidecar); !os.IsNotExist(err) {
			t.Fatal("read-only preview created a sidecar", sidecar, err)
		}
	}
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
	if plan["legacy_enabled"] != true || plan["same_native_dataset"] != false || plan["receipt_records"] != float64(3) || plan["unresolved_receipts"] != float64(2) ||
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
	// The legacy archive is an independent, local snapshot. Listing it never
	// reaches the native client, and its cursor is scoped to both the imported
	// connection and the selected filter.
	req("GET", legacyReceiptRoute("one")+"?limit=0", nil, false, 400)
	req("GET", legacyReceiptRoute("one")+"?limit=201", nil, false, 400)
	req("GET", legacyReceiptRoute("one")+"?after=not-a-cursor", nil, false, 400)
	legacyPage := req("GET", legacyReceiptRoute("one")+"?limit=2", nil, false, 200)
	if legacyPage["coverage"] != "legacy_snapshot" || legacyPage["receipt_count"] != float64(3) || legacyPage["pending_count"] != float64(2) || legacyPage["snapshot"] == nil {
		t.Fatal("legacy receipt page omitted snapshot metadata", legacyPage)
	}
	legacyItems, ok := legacyPage["items"].([]any)
	if !ok || len(legacyItems) != 2 {
		t.Fatal("legacy receipt page did not paginate", legacyPage)
	}
	firstLegacy := legacyItems[0].(map[string]any)
	if firstLegacy["idempotency_key"] != "legacy-accepted" || firstLegacy["state"] != "accepted" || firstLegacy["lookup"] != "legacy_snapshot" {
		t.Fatal("terminal receipt was not preserved as an archive snapshot", firstLegacy)
	}
	if unknown := legacyItems[1].(map[string]any); unknown["idempotency_key"] != "legacy-operation-unknown" || unknown["state"] != "submission_unknown" || unknown["lookup"] != "legacy_snapshot" {
		t.Fatal("old retry key was not retained as a pending archive snapshot", unknown)
	}
	legacyCursor, ok := legacyPage["next_after"].(string)
	if !ok || legacyCursor == "" {
		t.Fatal("legacy receipt cursor missing", legacyPage)
	}
	req("GET", legacyReceiptRoute("one")+"?after="+url.QueryEscape(legacyCursor)+"&pending=1", nil, false, 409)
	legacySecond := req("GET", legacyReceiptRoute("one")+"?limit=2&after="+url.QueryEscape(legacyCursor), nil, false, 200)
	secondItems := legacySecond["items"].([]any)
	if len(secondItems) != 1 || secondItems[0].(map[string]any)["idempotency_key"] != "legacy-pending" || secondItems[0].(map[string]any)["state"] != "submission_unknown" || legacySecond["next_after"] != nil {
		t.Fatal("legacy receipt pagination did not retain unresolved snapshot", legacySecond)
	}
	pendingOnly := req("GET", legacyReceiptRoute("one")+"?pending=1", nil, false, 200)
	if pendingOnly["receipt_count"] != float64(3) || pendingOnly["pending_count"] != float64(2) || len(pendingOnly["items"].([]any)) != 2 || pendingOnly["items"].([]any)[0].(map[string]any)["idempotency_key"] != "legacy-operation-unknown" {
		t.Fatal("pending legacy receipt filter was not local and stable", pendingOnly)
	}
	if unresolved := req("GET", legacyReceiptRoute("one")+"/legacy-operation-unknown", nil, false, 200); unresolved["state"] != "submission_unknown" || unresolved["lookup"] != "legacy_operation_unknown" {
		t.Fatal("archive record without its original operation was not retained locally", unresolved)
	}
	if mismatched := req("GET", legacyReceiptRoute("one")+"/legacy-pending", nil, false, 200); mismatched["state"] != "submission_unknown" || mismatched["lookup"] != "legacy_dataset_mismatch" || receiptReads.Load() != 0 {
		t.Fatal("foreign native dataset changed an imported receipt", mismatched)
	}
	daemonOffline.Store(true)
	if offline := req("GET", legacyReceiptRoute("one")+"/legacy-pending", nil, false, 200); offline["state"] != "submission_unknown" || offline["lookup"] != "legacy_native_unavailable" || receiptReads.Load() != 0 {
		t.Fatal("native outage changed the imported receipt", offline)
	}
	daemonOffline.Store(false)
	if receiptReads.Load() != 0 || sideEffects.Load() != 0 {
		t.Fatal("legacy receipt list replayed an old delivery", receiptReads.Load(), sideEffects.Load())
	}
	four := req("GET", planRoute("four"), nil, false, 200)
	if four["same_native_dataset"] != true {
		t.Fatal(four)
	}
	req("POST", planRoute("four"), planDigest(four), true, 200)
	// A valid cursor from another imported connection is stale for this archive,
	// rather than a license to enumerate a different account's old receipts.
	req("GET", legacyReceiptRoute("four")+"?after="+url.QueryEscape(legacyCursor), nil, false, 409)
	if recovered := req("GET", route("four")+"/receipts/legacy-pending", nil, false, 200); recovered["lookup"] != "platform" || recovered["server_id"] != "69234" {
		t.Fatal(recovered)
	}
	// A current checkpoint can hold the same key with a different native job.
	// The dedicated archive path must still resolve only the old operation.
	currentFour := filepath.Join(app, "workers", wechatapp.ConnectionID("four"), "queue", "messages-v2.json")
	write(currentFour, []byte(`{"version":1,"server":"new-dataset","account":"four","deliveries":[{"key":"legacy-pending","id":"current-job","thread":"filehelper","state":"accepted","server_id":"70000","input":{"account":"four","thread":"filehelper"}}]}`))
	if current := req("GET", route("four")+"/receipts/legacy-pending", nil, false, 200); current["lookup"] != "stored" || current["operation_id"] != "current-job" || current["server_id"] != "70000" {
		t.Fatal("current receipt lookup did not use its own checkpoint", current)
	}
	if archivePending := req("GET", legacyReceiptRoute("four")+"/legacy-pending", nil, false, 200); archivePending["lookup"] != "platform" || archivePending["server_id"] != "69234" {
		t.Fatal("matching legacy operation did not use the bounded lookup", archivePending)
	}
	if archiveAccepted := req("GET", legacyReceiptRoute("four")+"/legacy-accepted", nil, false, 200); archiveAccepted["lookup"] != "legacy_snapshot" || archiveAccepted["state"] != "accepted" {
		t.Fatal("terminal legacy receipt consulted the native queue", archiveAccepted)
	}
	if receiptReads.Load() != 2 || sideEffects.Load() != 0 {
		t.Fatal("legacy receipt lookup ran an unexpected operation", receiptReads.Load(), sideEffects.Load())
	}
	unknownCode, unknown := f.request("GET", legacyReceiptRoute("four")+"/missing-legacy-operation", nil, false)
	if unknownCode != http.StatusNotFound || receiptReads.Load() != 2 {
		t.Fatal("unknown legacy key was not contained by the archive guard", unknownCode, unknown)
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
	selectedRoute := memoryRoute("one") + "/123@chatroom"
	noteRoute := selectedRoute + "/preference"
	req("GET", memoryRoute("one")+"/filehelper/preference", nil, false, 400)
	groupReplyRoute := ruleRoute("one", "124@chatroom")
	req("GET", ruleRoute("one", "filehelper"), nil, false, 400)
	if row := req("GET", groupReplyRoute, nil, false, 200); row["status"] != "not_ready" || row["reply_settings"] != nil {
		t.Fatal("ungranted group disclosed its reply rule", row)
	}
	if row := req("GET", noteRoute, nil, false, 200); row["status"] != "not_ready" || row["note"] != nil {
		t.Fatal("ungranted group disclosed old preference", row)
	}
	otherRoute := memoryRoute("one") + "/filehelper"
	if row := req("GET", selectedRoute, nil, false, 200); row["status"] != "not_ready" {
		t.Fatal("selected history was not explicitly authorized", row)
	}
	setHistory := func(threads ...string) {
		req("PUT", route("one")+"/policy", map[string]any{
			"reply_threads":   []string{"123@chatroom", "filehelper"},
			"history_threads": threads,
		}, true, 200)
	}
	setHistory("123@chatroom", "filehelper")
	req("GET", memoryRoute("one")+"?after="+cursor, nil, false, 409)
	audit = req("GET", memoryRoute("one")+"?limit=1", nil, false, 200)
	if row := audit["conversations"].([]any)[0].(map[string]any); row["history_granted"] != true {
		t.Fatal("preview did not show configured target authorization", row)
	}
	if row := req("GET", selectedRoute, nil, false, 200); row["status"] != "not_ready" {
		t.Fatal("signed-out target passed cutover", row)
	}
	if row := req("GET", noteRoute, nil, false, 200); row["status"] != "not_ready" || row["note"] != nil {
		t.Fatal("signed-out target disclosed old preference", row)
	}
	loginOnline.Store(true)
	selected := req("GET", selectedRoute, nil, false, 200)
	if selected["status"] != "ready" || selected["event_count"] != float64(1) || bytes.Contains(j(selected), []byte("synthetic-private-body")) {
		t.Fatal("selected scope preview was wrong or leaked content", selected)
	}
	if row := req("GET", otherRoute, nil, false, 200); row["status"] != "empty" {
		t.Fatal("cleared old scope was treated as importable", row)
	}
	bundlePath := filepath.Join(target, stationLegacyMemoryFile("one", "123@chatroom"))
	if _, err := os.Stat(bundlePath); !os.IsNotExist(err) {
		t.Fatal("read-only scope preview created a bundle", err)
	}
	postMemory := func(plan map[string]any, sameIdentity bool) map[string]any {
		return map[string]any{"digest": plan["digest"], "same_identity": sameIdentity}
	}
	req("POST", selectedRoute, postMemory(selected, true), false, 403)
	req("POST", selectedRoute, postMemory(selected, false), true, 400)
	oldNotePreview := req("GET", noteRoute, nil, false, 200)
	if oldNotePreview["status"] != "ready" || oldNotePreview["note"] != oldGroupNote {
		t.Fatal("old note should be readable before importing the history bundle", oldNotePreview)
	}
	setHistory("filehelper")
	req("POST", selectedRoute, postMemory(selected, true), true, 409)
	req("POST", noteRoute, postMemory(oldNotePreview, true), true, 409)
	setHistory("123@chatroom", "filehelper")
	req("POST", selectedRoute, postMemory(selected, true), true, 409)
	req("POST", noteRoute, postMemory(oldNotePreview, true), true, 409)
	selected = req("GET", selectedRoute, nil, false, 200)

	targetMemory, err := companion.NewStore(filepath.Join(target, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	if err := targetMemory.Observe(companion.Input{Channel: "wechat", Account: "one", Thread: "filehelper", Sender: "synthetic-target", TraceID: "target-local-message", Text: "existing target history"}, cfg, now); err != nil {
		t.Fatal(err)
	}
	if err := targetMemory.Close(); err != nil {
		t.Fatal(err)
	}
	if row := req("GET", otherRoute, nil, false, 200); row["status"] != "conflict" {
		t.Fatal("existing target scope was not protected", row)
	}
	write(liveMarker, j(starting))
	req("POST", selectedRoute, postMemory(selected, true), true, 409)
	write(liveMarker, j(marker))
	oldMemory, err := companion.NewStore(legacyMemoryDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := oldMemory.Observe(companion.Input{Channel: "wechat", Account: "one", Thread: "123@chatroom", Sender: "synthetic-sender",
		TraceID: "added-after-preview", Text: "synthetic newer original", MessageTime: now.Format(time.RFC3339Nano),
		Source: &companion.SourceRef{Integrity: "observed", MessageType: 1, ServerID: "synthetic-second-original"}}, cfg, now); err != nil {
		t.Fatal(err)
	}
	if err := oldMemory.Close(); err != nil {
		t.Fatal(err)
	}
	req("POST", selectedRoute, postMemory(selected, true), true, 409)
	selected = req("GET", selectedRoute, nil, false, 200)
	if selected["status"] != "ready" || selected["event_count"] != float64(2) {
		t.Fatal("updated source was not reprojected", selected)
	}
	confirmed := req("POST", selectedRoute, postMemory(selected, true), true, 200)
	if confirmed["status"] != "applied" || confirmed["event_count"] != float64(2) || bytes.Contains(j(confirmed), []byte("synthetic-private-body")) {
		t.Fatal("unexpected apply result", confirmed)
	}
	rawBundle, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	req("POST", selectedRoute, postMemory(selected, true), true, 200)
	if data, err := os.ReadFile(bundlePath); err != nil || !bytes.Equal(rawBundle, data) {
		t.Fatal("idempotent apply changed the saved bundle", err)
	}
	if _, err := os.Stat(filepath.Join(target, stationLegacyMemoryFile("one", "filehelper"))); !os.IsNotExist(err) {
		t.Fatal("apply wrote a second conversation", err)
	}
	var bundle stationLegacyMemory
	if err := json.Unmarshal(rawBundle, &bundle); err != nil || bundle.Account != "one" || bundle.Thread != "123@chatroom" || len(bundle.Selection.Events) != 2 {
		t.Fatal("saved bundle scope mismatch", err)
	}
	if _, err := readStationLegacyMemory(target, "one", "123@chatroom", bundle.Grant); err != nil {
		t.Fatal("current grant could not open the selected bundle", err)
	}
	setNote := func(note string, summarize bool) {
		t.Helper()
		req("PUT", route("one")+"/policy", map[string]any{
			"reply_threads": []string{"123@chatroom", "filehelper"}, "history_threads": []string{"123@chatroom", "filehelper"},
			"attachment_threads": []string{"123@chatroom"},
			"group_rules":        []apphost.ConnectionGroupRule{{ThreadID: "123@chatroom", Note: note, SummarizeShares: summarize}},
		}, true, 200)
	}
	checkNote := func(note string, summarize bool) {
		t.Helper()
		detail := detailFor("one")
		rows, _ := detail["configured_policy"].(map[string]any)["group_rules"].([]any)
		if note == "" && !summarize {
			if len(rows) != 0 {
				t.Fatal("cleared target note reappeared", rows)
			}
			return
		}
		if len(rows) != 1 {
			t.Fatal("unexpected group rules", rows)
		}
		rule := rows[0].(map[string]any)
		if rule["note"] != note || rule["summarize_shares"] != summarize || detail["worker"] != "paused" {
			t.Fatal("target note or independent share grant changed", rule)
		}
	}
	notePreview := req("GET", noteRoute, nil, false, 200)
	if notePreview["status"] != "ready" || notePreview["note"] != oldGroupNote {
		t.Fatal("old scoped note unavailable after history import", notePreview)
	}
	req("POST", noteRoute, postMemory(notePreview, true), false, 403)
	req("POST", noteRoute, postMemory(notePreview, false), true, 400)
	setNote("合成新站备注", true)
	req("POST", noteRoute, postMemory(notePreview, true), true, 409)
	checkNote("合成新站备注", true)
	setNote("", false)
	notePreview = req("GET", noteRoute, nil, false, 200)
	write(liveMarker, j(starting))
	req("POST", noteRoute, postMemory(notePreview, true), true, 409)
	write(liveMarker, j(marker))
	notePreview = req("GET", noteRoute, nil, false, 200)
	req("POST", noteRoute, postMemory(notePreview, true), true, 200)
	checkNote(oldGroupNote, false)
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h, f = open()
	f.login()
	assertCutoverOwnerState(t, f, ownerIdentity, speakingVoice)
	checkNote(oldGroupNote, false)
	setNote("", false)
	req("POST", noteRoute, postMemory(notePreview, true), true, 200)
	checkNote("", false)
	// This group has a config rule and no SQLite scope. A reply grant, but
	// no attachment or history grant, is required to inspect its old settings.
	setRule := func(includeGroup bool, settings any) {
		t.Helper()
		threads := []string{"123@chatroom", "125@chatroom", "filehelper"}
		body := map[string]any{"reply_threads": threads}
		if includeGroup {
			body["reply_threads"] = append(threads, "124@chatroom")
			row := map[string]any{"thread_id": "124@chatroom", "note": "independent note"}
			if settings != nil {
				row["reply_settings"] = settings
			}
			body["group_rules"] = []any{row}
		}
		req("PUT", route("one")+"/policy", body, true, 200)
	}
	setRule(true, nil)
	if row := req("GET", ruleRoute("one", "125@chatroom"), nil, false, 200); row["status"] != "unsupported" || row["reply_settings"] != nil {
		t.Fatal("social mode was treated as an active grant", row)
	}
	if row := req("GET", ruleRoute("four", "124@chatroom"), nil, false, 200); row["status"] != "not_ready" || row["reply_settings"] != nil {
		t.Fatal("another connection reused a group grant", row)
	}
	rulePreview := req("GET", groupReplyRoute, nil, false, 200)
	expectedRule := map[string]any{"mode": "all", "prefix": "/group ", "persona": "synthetic scoped role", "force_reply_when_mentioned": true}
	if rulePreview["status"] != "ready" || !bytes.Equal(j(rulePreview["reply_settings"]), j(expectedRule)) || bytes.Contains(j(rulePreview), []byte("do-not-copy")) {
		t.Fatal("config-only rule preview was wrong", rulePreview)
	}
	req("POST", groupReplyRoute, postMemory(rulePreview, true), false, 403)
	req("POST", groupReplyRoute, postMemory(rulePreview, false), true, 400)
	write(configPath, []byte(strings.Replace(rawConfig, "synthetic scoped role", "changed synthetic role", 1)))
	req("POST", groupReplyRoute, postMemory(rulePreview, true), true, 409)
	write(configPath, []byte(rawConfig))
	setRule(false, nil)
	req("POST", groupReplyRoute, postMemory(rulePreview, true), true, 409)
	setRule(true, nil)
	req("POST", groupReplyRoute, postMemory(rulePreview, true), true, 409)
	rulePreview = req("GET", groupReplyRoute, nil, false, 200)
	req("POST", groupReplyRoute, postMemory(rulePreview, true), true, 200)
	checkRule := func(expect any) {
		t.Helper()
		detail := detailFor("one")
		rows := detail["configured_policy"].(map[string]any)["group_rules"].([]any)
		if len(rows) != 1 || detail["worker"] != "paused" {
			t.Fatal("unexpected policy or worker state after rule import", detail)
		}
		row := rows[0].(map[string]any)
		if row["thread_id"] != "124@chatroom" || row["note"] != "independent note" || row["summarize_shares"] != false || !bytes.Equal(j(row["reply_settings"]), j(expect)) {
			t.Fatal("rule import overwrote independent settings", row)
		}
	}
	checkRule(expectedRule)
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h, f = open()
	f.login()
	checkRule(expectedRule)
	setRule(true, nil)
	checkRule(expectedRule)
	setRule(true, map[string]string{"mode": "mention"})
	req("POST", groupReplyRoute, postMemory(rulePreview, true), true, 200)
	checkRule(nil)
	if row := req("GET", selectedRoute, nil, false, 200); row["status"] != "applied" {
		t.Fatal("applied scope was not restored after restart", row)
	}
	activeNative, err := h.chat.wechat.WorkerSender(t.Context(), wechatapp.ConnectionID("one"))
	if err != nil {
		t.Fatal(err)
	}
	liveMemory, err := companion.NewStore(filepath.Join(target, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	defer liveMemory.Close()
	tools := &stationAttachmentTools{dir: target, native: activeNative, store: liveMemory, blocked: func() bool { return false },
		reads: map[string]time.Time{"123@chatroom": bundle.Grant}}
	trace := companion.Input{Channel: "wechat", Account: "one", Thread: "123@chatroom", Sender: "synthetic-sender",
		TraceID: "after-import", Text: "synthetic new question", MessageTime: bundle.Grant.Add(time.Second).Format(time.RFC3339Nano)}
	tools.SetTrace(trace)
	stamp := tools.MemoryResultAuthorization()
	if stamp == "" || !tools.MemoryResultAuthorized(stamp) || !tools.EphemeralToolResult("wechat_read_legacy") {
		t.Fatal("imported evidence was not bound to the live history grant")
	}
	list, err := tools.List(t.Context())
	if err != nil || !slices.ContainsFunc(list, func(tool mcptools.Tool) bool { return tool.Name == "wechat_read_legacy" }) {
		t.Fatal("ordinary tool discovery did not expose the current authorized scope", err)
	}
	call := json.RawMessage(`{"kind":"events","max_bytes":1000}`)
	if raw, err := tools.Call(t.Context(), "wechat_read_legacy", call); err != nil ||
		!strings.Contains(raw, "synthetic-private-body") || strings.Contains(raw, "foreign-private-thread") {
		t.Fatal("ordinary tool access to imported evidence was incorrect", err)
	}
	if err := liveMemory.Observe(trace, cfg, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := liveMemory.Clear(companion.ScopeID("wechat", "one", trace.Thread)); err != nil {
		t.Fatal(err)
	}
	if raw, err := tools.Call(t.Context(), "wechat_read_legacy", call); err == nil || raw != "" {
		t.Fatal("scope clear did not block never-observed imported sources")
	}
	if tools.MemoryResultAuthorized(stamp) {
		t.Fatal("a queued reply can still use the cleared evidence authority")
	}
	setHistory("filehelper")
	req("POST", selectedRoute, postMemory(selected, true), true, 409)
	setHistory("123@chatroom", "filehelper")
	if row := req("GET", selectedRoute, nil, false, 200); row["status"] != "conflict" {
		t.Fatal("regrant reactivated the bundle from the old grant", row)
	}
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
		req("GET", legacyReceiptRoute("one")+"?limit=200", nil, false, 200)["receipt_count"] != float64(3) ||
		req("GET", route("one"), nil, false, 200)["connection"].(map[string]any)["worker"] != "paused" ||
		sideEffects.Load() != 0 || receiptReads.Load() != 2 {
		t.Fatal("migration was not durable or triggered native side effects")
	}
}

func planDigest(plan map[string]any) map[string]any { return map[string]any{"digest": plan["digest"]} }
