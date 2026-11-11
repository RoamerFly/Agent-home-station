package station

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/stationruntime"
	"wechat-bot/internal/websession"
)

func TestStationSurfScheduleClaimPauseAndRestart(t *testing.T) {
	w := &stationWechatWorker{dir: t.TempDir()}
	const account = "synthetic-one"
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	settings := stationSurfDefaults(account).Settings
	settings.Timezone, settings.QuietStart, settings.QuietEnd = "UTC", "10:00", "11:00"
	settings.IntervalMinutes, settings.IntervalMaxMinutes, settings.DailyLimit = 15, 15, 1
	write := stationSurfUpdate{Action: "save", Settings: &settings, Key: "save-one"}
	if !write.valid() {
		t.Fatal("valid schedule rejected")
	}
	state, err := w.updateSurf(account, write, false, now)
	if err != nil || state.Enabled || state.Revision != 1 {
		t.Fatalf("save enabled schedule or lost revision: %+v %v", state, err)
	}
	replay, err := w.updateSurf(account, write, false, now.Add(time.Minute))
	if err != nil || !reflect.DeepEqual(replay, state) {
		t.Fatalf("save replay mutated state: %+v %v", replay, err)
	}
	write.Settings.DailyLimit = 2
	if _, err := w.updateSurf(account, write, true, now); !errors.Is(err, apphost.ErrLifecycleConflict) {
		t.Fatalf("mutated request reused key: %v", err)
	}
	resume := stationSurfUpdate{Action: "resume", Revision: 1, Key: "resume-one", AllowPublicReads: true}
	if _, err := w.updateSurf(account, resume, false, now); !errors.Is(err, apphost.ErrLifecycleNotReady) {
		t.Fatalf("resumed without reader: %v", err)
	}
	state, err = w.updateSurf(account, resume, true, now)
	if err != nil || !state.Enabled || !state.Next.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("resume did not schedule: %+v %v", state, err)
	}
	// A reconstructed worker recovers the same schedule and cannot substitute
	// another registered account for the file's original owner.
	w = &stationWechatWorker{dir: w.dir}
	if _, err := w.surfSchedule("synthetic-two"); err == nil {
		t.Fatal("accepted another account's schedule")
	}
	prepared := 0
	prepare := func(context.Context, stationSurfSchedule) (func(context.Context, string) (companion.ToolAccess, error), error) {
		prepared++
		return func(context.Context, string) (companion.ToolAccess, error) { return nil, nil }, nil
	}
	claim := func(at time.Time) bool {
		t.Helper()
		task, err := w.claimSurf(t.Context(), account, at, func() error { return nil }, prepare)
		if err != nil {
			t.Fatal(err)
		}
		if task != nil && (task.Thread != "" || task.ForceShare || task.PrivateBrowseTools == nil) {
			t.Fatal("claim contains a delivery route")
		}
		return task != nil
	}
	if claim(now) || claim(now.Add(time.Hour)) || !claim(now.Add(2*time.Hour)) || claim(now.Add(3*time.Hour)) ||
		!claim(now.Add(24*time.Hour)) || prepared != 2 {
		t.Fatal("schedule did not honor persisted time, quiet period or daily budget")
	}
	runCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w.surfCancel = cancel
	pause := stationSurfUpdate{Action: "pause", Revision: 2, Key: "pause-one"}
	state, err = w.updateSurf(account, pause, true, now)
	if err != nil || state.Enabled || !state.Next.IsZero() || runCtx.Err() == nil || claim(now.Add(48*time.Hour)) {
		t.Fatalf("pause did not revoke scheduled and active work: %+v %v", state, err)
	}
}

func TestStationSurfOwnerRoute(t *testing.T) {
	base := t.TempDir()
	data, home := filepath.Join(base, "host"), filepath.Join(base, "home")
	native := filepath.Join(data, "apps", "wechat", "native")
	mustWriteSnapshotFixture(t, filepath.Join(native, "api-token"), []byte("synthetic-surf-native-token"))
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("surf settings attempted native mutation: %s", r.Method)
		}
		var result any
		switch r.URL.Path {
		case "/v1/status":
			result = map[string]any{"pid": 9142, "dataDir": native, "serverId": "synthetic-surf", "ownershipGeneration": "synthetic-generation"}
		case "/v1/accounts":
			result = map[string]any{"items": []map[string]string{{"id": "one"}, {"id": "two"}}}
		case "/v1/accounts/one", "/v1/accounts/two":
			result = map[string]any{"id": filepath.Base(r.URL.Path), "storageMode": "managed", "connection": "offline"}
		default:
			t.Errorf("unexpected surf native route %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	t.Cleanup(daemon.Close)
	entry, err := wechatapp.StationRegistration(t.Context(), daemon.URL, 9142, data)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := apphost.New(entry)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewLocalHomeHandlerWithApps(data, home, "synthetic-local-password", "", registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	jar, _ := cookiejar.New(nil)
	f := &localHomeFixture{t: t, server: server, client: &http.Client{Jar: jar}}
	url := func(account string) string {
		return "/api/apps/wechat/connections/" + wechatapp.ConnectionID(account) + "/surf"
	}
	req := func(method, path string, input any, csrf bool, status int) map[string]any {
		t.Helper()
		got, out := f.request(method, path, input, csrf)
		if got != status {
			t.Fatalf("%s %s: %d want %d: %+v", method, path, got, status, out)
		}
		return out
	}
	req("GET", url("one"), nil, false, 401)
	f.login()
	req("GET", url("one"), nil, false, 200)
	empty := req("GET", strings.TrimSuffix(url("one"), "/surf")+"/surf-history?limit=25", nil, false, 200)
	if empty["limit"] != float64(25) || len(empty["rows"].([]any)) != 0 {
		t.Fatal("empty history lost requested parameters", empty)
	}
	req("GET", strings.TrimSuffix(url("one"), "/surf")+"/surf-history?limit=0", nil, false, 400)
	if _, err := os.Stat(filepath.Join(data, "apps", "wechat", "workers")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opening settings created a worker: %v", err)
	}
	settings := stationSurfDefaults("one").Settings
	settings.Topics = []string{"合成主题一"}
	input := stationSurfUpdate{Action: "save", Settings: &settings, Key: "synthetic-request"}
	req("PUT", url("one"), input, false, 403)
	saved := req("PUT", url("one"), input, true, 200)
	if saved["enabled"] != false || saved["revision"] != float64(1) || saved["last_key"] != nil {
		t.Fatal(saved)
	}
	req("PUT", url("one"), input, true, 200)
	input.Key = "stale-request"
	req("PUT", url("one"), input, true, 409)
	req("PUT", url("one"), stationSurfUpdate{Action: "resume", Revision: 1, Key: "resume"}, true, 400)
	req("PUT", url("one"), stationSurfUpdate{Action: "resume", Revision: 1, Key: "resume", AllowPublicReads: true}, true, 409)
	other := req("GET", url("two"), nil, false, 200)
	if other["account"] != "two" || other["revision"] != float64(0) {
		t.Fatal(other)
	}
}

func TestStationSurfManagedReaderWindows(t *testing.T) {
	packaged := os.Getenv("AHS_TEST_SURF_PACKAGE")
	if runtime.GOOS != "windows" || packaged == "" {
		t.Skip("isolated Windows web reader with AHS_TEST_SURF_PACKAGE")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
	defer cancel()
	worker := &stationWechatWorker{dir: t.TempDir()}
	host := &stationWechat{ctx: ctx, shareOptions: stationruntime.PackagedToolOptionsAt(packaged)}
	const account, feed = "synthetic-one", "https://synthetic.example/feed"
	web, err := host.shareHost(worker, account)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = web.controller.Close(); _ = web.store.Close() })
	prepared, err := web.store.Prepare(ctx, filepath.Join(packaged, "plugins", stationSurfPlugin))
	if err != nil {
		t.Fatal(err)
	}
	item, err := web.store.Install(ctx, prepared.Token, prepared.Digest, []string{"network"})
	if err != nil {
		t.Fatal(err)
	}
	if err := web.controller.EnableOnDemand(ctx, stationSurfPlugin, item.Revision); err != nil {
		t.Fatal(err)
	}
	var traffic []string
	const pageURL = "https://synthetic.example/article"
	web.surfRead = func(_ context.Context, source string, _ int64, _ time.Duration) ([]byte, string, string, error) {
		traffic = append(traffic, source)
		switch source {
		case feed:
			return []byte(`<rss><channel><item><title>合成收藏</title><link>https://synthetic.example/article</link><description>合成摘要</description></item></channel></rss>`), "application/rss+xml", source, nil
		case pageURL:
			return []byte(`<html><head><title>合成收藏</title></head><body><p>这里是一段只在合成网页正文出现的文字。</p></body></html>`), "text/html; charset=utf-8", source, nil
		}
		return nil, "", "", errors.New("unexpected public read")
	}
	now := time.Now()
	settings := stationSurfDefaults(account).Settings
	settings.QuietStart, settings.QuietEnd = "00:00", "00:00"
	settings.IntervalMinutes, settings.IntervalMaxMinutes = 15, 15
	settings.Sources = []mcptools.DiscoverySource{{ID: "synthetic", Name: "合成站点", Type: "rss", Kind: "text", URL: feed, Enabled: true}}
	if _, err := worker.updateSurf(account, stationSurfUpdate{Action: "save", Settings: &settings, Key: "save"}, true, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.updateSurf(account, stationSurfUpdate{Action: "resume", Revision: 1, AllowPublicReads: true, Key: "resume"}, true, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	active := func() error { return nil }
	task, err := worker.claimSurf(ctx, account, now, active, func(ctx context.Context, state stationSurfSchedule) (func(context.Context, string) (companion.ToolAccess, error), error) {
		return host.prepareSurf(ctx, worker, state, active)
	})
	if err != nil || task == nil {
		t.Fatalf("scheduled claim unavailable: %v", err)
	}
	access, err := task.PrivateBrowseTools(ctx, "surf-synthetic")
	if err != nil {
		t.Fatal(err)
	}
	tools := access.(*stationSurfTools)
	run, stop, err := tools.BindSurfBrowseContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	input := companion.Input{Channel: "host", Account: account, Thread: "host:surf:" + account, TraceID: "surf-synthetic", Mode: "direct", Preview: true, Surf: true, Text: task.Prompt()}
	tools.SetTrace(input)
	list, err := tools.List(run)
	if err != nil || len(list) != 2 || list[0].Name != "web_discover" || list[1].Name != "web_fetch" {
		t.Fatalf("unexpected private tool set: %+v %v", list, err)
	}
	if _, err := tools.Call(run, "web_fetch", json.RawMessage(`{"candidate_id":"fake"}`)); err == nil || len(traffic) != 0 {
		t.Fatal("unbound candidate reached the network")
	}
	if _, err := tools.Call(ctx, "web_discover", json.RawMessage(`{"source_id":"synthetic","limit":1}`)); err == nil || len(traffic) != 0 {
		t.Fatal("tool called outside its original context")
	}
	result, err := tools.Call(run, "web_discover", json.RawMessage(`{"source_id":"synthetic","limit":1}`))
	var discovery struct {
		Candidates []mcptools.DiscoveryItem `json:"candidates"`
	}
	if err != nil || json.Unmarshal([]byte(result), &discovery) != nil || len(discovery.Candidates) != 1 {
		t.Fatalf("discover: %s %v", result, err)
	}
	id := discovery.Candidates[0].ID
	args, _ := json.Marshal(map[string]any{"candidate_id": id})
	body, err := tools.Call(run, "web_fetch", args)
	if err != nil || !strings.Contains(body, "只在合成网页正文出现") || !reflect.DeepEqual(traffic, []string{feed, pageURL}) {
		t.Fatalf("read: %s %+v %v", body, traffic, err)
	}
	var full struct {
		Hash string `json:"sha256"`
	}
	if json.Unmarshal([]byte(body), &full) != nil || full.Hash == "" {
		t.Fatal("full body lost its hash")
	}
	partial, _ := json.Marshal(map[string]any{"candidate_id": id, "offset": 5, "limit": 5, "expected_sha256": full.Hash})
	if _, err := tools.Call(run, "web_fetch", partial); err != nil {
		t.Fatal(err)
	}
	model := &stationSurfModel{t: t, candidate: id}
	config := companion.Defaults()
	config.Enabled = true
	config.Permissions = companion.Permissions{ReplyText: true}
	engine, err := companion.NewPreview(config, model, "合成公开浏览")
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools, engine.MaxToolCalls = tools, task.PrivateToolCalls
	decision, err := engine.Respond(run, input)
	state, loadErr := worker.surfSchedule(account)
	if err != nil || loadErr != nil || model.calls != 4 || decision.SurfAction != "bookmark" || state.LastRun == nil || state.LastRun.Bookmark != "合成收藏" {
		t.Fatalf("bookmark lost: %+v %+v %v %v", decision, state.LastRun, err, loadErr)
	}
	page, err := worker.surfHistory(run, web, account, "", 1)
	if err != nil || len(page.Rows) != 1 || !page.Rows[0].Readable || !page.Rows[0].Bookmarked || page.Next == "" {
		t.Fatalf("history missing verified bookmark: %+v %v", page, err)
	}
	wire, _ := json.Marshal(page)
	if strings.Contains(string(wire), "只在合成网页正文出现") {
		t.Fatal("history list returned private body")
	}
	saved, err := worker.readSurfHistoryBody(run, web, account, page.Rows[0].Job, page.SourceVersion)
	if err != nil || !strings.Contains(string(saved), "只在合成网页正文出现") {
		t.Fatalf("retained body unavailable: %s %v", saved, err)
	}
	if _, err := worker.readSurfHistoryBody(run, web, "synthetic-two", page.Rows[0].Job, page.SourceVersion); err == nil {
		t.Fatal("another account read private browser evidence")
	}
	second, err := worker.surfHistory(run, web, account, page.Next, 1)
	if err != nil || len(second.Rows) != 1 || second.Rows[0].Offset != 5 || second.Rows[0].Returned != 5 || second.Next != "" {
		t.Fatalf("continuation lost exact read range: %+v %v", second, err)
	}
	if _, err := worker.updateSurf(account, stationSurfUpdate{Action: "pause", Revision: 2, Key: "pause"}, true, now); err != nil || run.Err() == nil {
		t.Fatalf("pause did not revoke tool context: %v", err)
	}
	if _, err := tools.Call(run, "web_fetch", args); err == nil || len(traffic) != 3 {
		t.Fatal("revoked request reached the network")
	}
	if _, err := worker.readSurfHistoryBody(ctx, web, account, page.Rows[0].Job, page.SourceVersion); err == nil {
		t.Fatal("old body reference survived revocation")
	}
	if _, err := worker.surfHistory(ctx, web, account, page.Next, 1); err == nil {
		t.Fatal("old history cursor survived revocation")
	}
	if err := web.controller.Close(); err != nil {
		t.Fatal(err)
	}
	if err := web.store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := &stationWechatWorker{dir: worker.dir}
	reopened, err := host.shareHost(restarted, account)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.controller.Close(); _ = reopened.store.Close() })
	after, err := restarted.surfHistory(ctx, reopened, account, "", 10)
	if err != nil || len(after.Rows) != 2 || !after.Rows[0].Bookmarked || !after.Rows[0].Readable {
		t.Fatalf("restarted history lost bookmark or paused public evidence: %+v %v", after, err)
	}
	if _, err := restarted.readSurfHistoryBody(ctx, reopened, account, after.Rows[0].Job, after.SourceVersion); err != nil {
		t.Fatalf("paused scheduler denied a currently authorized archived source: %v", err)
	}
	installed, err := reopened.store.Installed(ctx, stationSurfPlugin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.store.SetEnabled(ctx, stationSurfPlugin, installed.Revision, false); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.readSurfHistoryBody(ctx, reopened, account, after.Rows[0].Job, after.SourceVersion); err == nil {
		t.Fatal("revoked public reader exposed a body")
	}
	withdrawn, err := restarted.surfHistory(ctx, reopened, account, "", 10)
	if err != nil || len(withdrawn.Rows) != 2 || withdrawn.Rows[0].Readable || !withdrawn.Rows[0].Bookmarked {
		t.Fatalf("revoked history lost summaries or exposed private results: %+v %v", withdrawn, err)
	}
	if len(traffic) != 3 {
		t.Fatal("retained history attempted a network read")
	}
	exerciseSurfHistoryOwnerHTTP(t, host, restarted, account, withdrawn.Rows[0].Job, withdrawn.SourceVersion)
}

type surfHistoryFixtureNative struct {
	stationWechatNative
	account string
}

func (f surfHistoryFixtureNative) Inspect(_ context.Context, id string) (apphost.ConnectionStatus, error) {
	if id != wechatapp.ConnectionID(f.account) {
		return apphost.ConnectionStatus{}, apphost.ErrNotFound
	}
	return apphost.ConnectionStatus{Connection: apphost.Connection{ID: id, AccountID: f.account}}, nil
}

func exerciseSurfHistoryOwnerHTTP(t *testing.T, host *stationWechat, worker *stationWechatWorker, account, job, version string) {
	t.Helper()
	owner, err := websession.New("synthetic-local-password", "", "synthetic-surf-history")
	if err != nil {
		t.Fatal(err)
	}
	host.stationWechatNative = surfHistoryFixtureNative{account: account}
	id := wechatapp.ConnectionID(account)
	host.workers = map[string]*stationWechatWorker{id: worker}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", owner.Login)
	path := "/api/apps/wechat/connections/{connection}/surf-history"
	mux.HandleFunc("GET "+path, owner.Protect(host.surfHistoryRoute(owner, false)))
	mux.HandleFunc("GET "+path+"/{job}", owner.Protect(host.surfHistoryRoute(owner, true)))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	jar, _ := cookiejar.New(nil)
	f := &localHomeFixture{t: t, server: server, client: &http.Client{Jar: jar}}
	url := "/api/apps/wechat/connections/" + id + "/surf-history"
	if status, _ := f.request("GET", url, nil, false); status != 401 {
		t.Fatal("history accepted anonymous read", status)
	}
	f.login()
	status, page := f.request("GET", url+"?limit=1", nil, false)
	if status != 200 || len(page["rows"].([]any)) != 1 || page["rows"].([]any)[0].(map[string]any)["readable"] != false {
		t.Fatal("owner history lost revoked evidence state", status, page)
	}
	if status, _ := f.request("GET", url+"/"+job+"?source_version="+version, nil, false); status != 409 {
		t.Fatal("owner body route bypassed revoked public reader", status)
	}
	if status, _ := f.request("GET", "/api/apps/wechat/connections/"+wechatapp.ConnectionID("another-account")+"/surf-history", nil, false); status != 404 {
		t.Fatal("history connection scope bypassed", status)
	}
	if status, _ := f.request("GET", url+"?limit=0", nil, false); status != 400 {
		t.Fatal("invalid limit accepted", status)
	}
}

type stationSurfModel struct {
	t         *testing.T
	candidate string
	calls     int
}

func (m *stationSurfModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", errors.New("synthetic surf model requires native functions")
}

func (m *stationSurfModel) ChatFunctions(_ context.Context, _ string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
	m.calls++
	raw, _ := json.Marshal(turns)
	switch m.calls {
	case 1:
		for _, tool := range tools {
			if tool.Name != "web_discover" && tool.Name != "web_fetch" && tool.Name != "discover_tools" {
				m.t.Fatalf("private observer received another application tool: %s", tool.Name)
			}
		}
		return `{"action":"tool","tool":"discover_tools","arguments":{"query":"web"}}`, nil
	case 2:
		return `{"action":"tool","tool":"web_discover","arguments":{"source_id":"synthetic","limit":1}}`, nil
	case 3:
		if !strings.Contains(string(raw), m.candidate) {
			return "", errors.New("verified candidate was absent from model turns")
		}
		body, _ := json.Marshal(map[string]any{"action": "tool", "tool": "web_fetch", "arguments": map[string]string{"candidate_id": m.candidate}})
		return string(body), nil
	case 4:
		if !strings.Contains(string(raw), "只在合成网页正文出现") {
			return "", errors.New("verified body was absent from model turns")
		}
		body, _ := json.Marshal(map[string]any{"action": "silent", "surf_action": "bookmark", "surf_item_id": m.candidate})
		return string(body), nil
	default:
		return "", errors.New("unexpected synthetic model call")
	}
}
