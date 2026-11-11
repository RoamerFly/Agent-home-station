package station

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
)

type surfShareFixture struct {
	t                       *testing.T
	f                       *localHomeFixture
	host                    *stationWechat
	worker                  *stationWechatWorker
	web                     *stationShareHost
	installed               pluginhost.InstalledPackage
	source                  mcptools.DiscoverySource
	path, id                string
	mu                      sync.Mutex
	dataset                 string
	statuses                map[string]map[string]any
	sends                   []map[string]string
	lose, hide, lookupError bool
	beforeSubmit            func()
	reconnect               func()
}

func newSurfShareFixture(t *testing.T) *surfShareFixture {
	t.Helper()
	x := &surfShareFixture{t: t, dataset: "synthetic-surf-dataset", statuses: map[string]map[string]any{}}
	base := t.TempDir()
	data, home := filepath.Join(base, "host"), filepath.Join(base, "home")
	native := filepath.Join(data, "apps", "wechat", "native")
	mustWriteSnapshotFixture(t, filepath.Join(native, "api-token"), []byte("synthetic-surf-share-token"))
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		x.mu.Lock()
		defer x.mu.Unlock()
		var out any
		switch r.URL.Path {
		case "/v1/status":
			out = map[string]any{"pid": 9182, "dataDir": native, "serverId": x.dataset, "ownershipGeneration": "synthetic-surf-generation"}
		case "/v1/accounts":
			out = map[string]any{"items": []map[string]any{{"id": "one", "authenticated": true, "connection": "online"}, {"id": "two", "authenticated": true, "connection": "online"}}}
		case "/v1/accounts/one", "/v1/accounts/two":
			out = map[string]any{"id": filepath.Base(r.URL.Path), "storageMode": "managed", "authenticated": true, "connection": "online"}
		case "/v1/accounts/one/sends/by-idempotency-key":
			if x.lookupError {
				http.Error(w, "synthetic lookup unavailable", 503)
				return
			}
			status, ok := x.statuses[r.URL.Query().Get("key")]
			if !ok || x.hide {
				http.NotFound(w, r)
				return
			}
			out = status
		case "/v1/accounts/one/sends":
			if r.Method != "POST" {
				t.Error("unexpected native method")
				http.NotFound(w, r)
				return
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			key := r.Header.Get("Idempotency-Key")
			if body["kind"] != "share" || key == "" {
				t.Error("share lost native link type/key", body)
			}
			x.sends = append(x.sends, body)
			out = map[string]any{"id": fmt.Sprintf("synthetic-share-%d", len(x.sends)), "accountId": "one", "threadId": body["threadId"], "kind": "send", "state": "accepted", "serverMessageId": fmt.Sprint(9100 + len(x.sends))}
			x.statuses[key] = out.(map[string]any)
			if fn := x.beforeSubmit; fn != nil {
				x.beforeSubmit = nil
				x.mu.Unlock()
				fn()
				x.mu.Lock()
			}
			if x.lose {
				http.Error(w, "synthetic lost submit reply", 503)
				return
			}
		default:
			if strings.HasPrefix(r.URL.Path, "/v1/accounts/one/jobs/") && strings.HasSuffix(r.URL.Path, "/cancel") {
				id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/accounts/one/jobs/"), "/cancel")
				for _, s := range x.statuses {
					if s["id"] == id {
						s["state"] = "cancelled"
						out = s
						break
					}
				}
				if out != nil {
					break
				}
			}
			t.Error("unexpected synthetic surf request", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(daemon.Close)
	entry, err := wechatapp.StationRegistration(t.Context(), daemon.URL, 9182, data)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := apphost.New(entry)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewLocalHomeHandlerWithApps(data, home, "synthetic-local-password", "", registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	jar, _ := cookiejar.New(nil)
	x.f = &localHomeFixture{t: t, server: server, client: &http.Client{Jar: jar}, handler: h, dir: data, root: home}
	x.f.login()
	x.request("POST", "/api/local/home", map[string]string{"action": "initialize"}, 200)
	x.id = wechatapp.ConnectionID("one")
	x.path = "/api/apps/wechat/connections/" + x.id
	x.request("PUT", x.path+"/policy", map[string]any{"reply_threads": []string{"target@chatroom", "second@chatroom"}}, 200)
	x.host = h.chat.wechat
	x.reconnect = func() {
		entry, err := wechatapp.StationRegistration(t.Context(), daemon.URL, 9182, data)
		if err != nil {
			t.Fatal(err)
		}
		x.host.stationWechatNative = entry.Provider.(stationWechatNative)
	}
	x.worker, err = x.host.worker(x.id, false)
	if err != nil {
		t.Fatal(err)
	}
	x.source = mcptools.DiscoverySource{ID: "synthetic", Name: "合成公开来源", Type: "rss", Kind: "text", URL: "https://synthetic.example/feed", Enabled: true}
	settings := stationSurfDefaults("one").Settings
	settings.Sources = []mcptools.DiscoverySource{x.source}
	settings.QuietStart, settings.QuietEnd = "00:00", "00:00"
	x.request("PUT", x.path+"/surf", stationSurfUpdate{Action: "save", Settings: &settings, Key: "synthetic-share-schedule"}, 200)
	x.web, err = x.host.shareHost(x.worker, "one")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := x.web.store.Prepare(t.Context(), filepath.Join("..", "..", "plugins", stationSurfPlugin))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := x.web.store.Install(t.Context(), prepared.Token, prepared.Digest, []string{"network"})
	if err != nil {
		t.Fatal(err)
	}
	x.installed, err = x.web.store.SetEnabled(t.Context(), stationSurfPlugin, installed.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func (x *surfShareFixture) request(method, path string, in any, want int) map[string]any {
	x.t.Helper()
	status, out := x.f.request(method, path, in, true)
	if status != want {
		x.t.Fatalf("%s %s = %d want %d: %+v", method, path, status, want, out)
	}
	return out
}

func (x *surfShareFixture) grant(revision int, key string, automatic bool, targets ...string) map[string]any {
	x.t.Helper()
	sources := []string{}
	if len(targets) > 0 {
		sources = []string{x.source.ID}
	}
	return x.request("PUT", x.path+"/surf-share-grants", stationSurfShareGrantInput{Revision: revision, Key: key, Auto: automatic, Targets: targets, Sources: sources}, 200)
}

func (x *surfShareFixture) job(name string) pluginhost.Job {
	x.t.Helper()
	ctx := x.t.Context()
	trace := "surf-" + name
	job, _, err := x.web.store.CreateJob(ctx, pluginhost.Job{Plugin: stationSurfPlugin, Version: x.installed.Package.Version, PackageDigest: x.installed.Digest, ConfigRevision: "0", Tool: "web.read", Context: pluginhost.Invocation{Account: "one", Thread: "host:surf:one", Requester: "host:surf", Purpose: "private public web browsing", Authorization: trace, RequestKey: name, EvidenceEpoch: localCheckpointKey(x.source), SourceVersions: map[string]int{trace: 1}}, Arguments: json.RawMessage(`{}`)})
	if err != nil {
		x.t.Fatal(err)
	}
	job, err = x.web.store.ClaimJob(ctx, job.ID, job.Revision)
	if err != nil {
		x.t.Fatal(err)
	}
	summary, _ := json.Marshal(stationSurfReadSummary{Verified: true, Title: "合成已读网页", SourceID: x.source.ID, Candidate: name, Returned: 7, Total: 7, Complete: true})
	private, _ := json.Marshal(map[string]any{"url": "https://synthetic.example/" + name, "sha256": hexDigest([]byte("合成网页原文")), "candidate_id": name, "text": "合成网页原文"})
	job, err = x.web.store.CompleteJobWithPrivate(ctx, job.ID, job.Revision, nil, summary, private)
	if err != nil {
		x.t.Fatal(err)
	}
	return job
}

func TestStationSurfShareAuthorizationAndDelivery(t *testing.T) {
	x := newSurfShareFixture(t)
	job := x.job("one")
	input := map[string]string{"job": job.ID, "target": "target@chatroom", "description": "合成分享说明"}
	x.request("POST", x.path+"/surf-shares", input, 409)
	if code, _ := x.f.request("PUT", x.path+"/surf-share-grants", stationSurfShareGrantInput{Key: "no-csrf"}, false); code != 403 {
		t.Fatal("grant bypassed CSRF", code)
	}
	grant := x.grant(0, "allow", false, "target@chatroom", "second@chatroom")
	if grant["current"] != true {
		t.Fatal("fresh grant not current", grant)
	}
	public := grant["grant"].(map[string]any)
	if len(public) != 4 || public["revision"] != float64(1) {
		t.Fatal("private grant metadata exposed", public)
	}
	x.grant(0, "allow", false, "target@chatroom", "second@chatroom") // exact replay
	x.request("PUT", x.path+"/surf-share-grants", stationSurfShareGrantInput{Revision: 0, Key: "stale"}, 409)
	accepted := x.request("POST", x.path+"/surf-shares", input, 200)
	key := accepted["idempotency_key"].(string)
	if accepted["state"] != "accepted" || accepted["server_id"] == nil || accepted["server_id"] == "" {
		t.Fatal("link receipt not accepted", accepted)
	}
	input["description"] = "重试时不能改变原文"
	x.request("POST", x.path+"/surf-shares", input, 200)
	if len(x.sends) != 1 || x.sends[0]["description"] != "合成分享说明" {
		t.Fatal("retry resubmitted or changed frozen payload", x.sends)
	}
	input["target"] = "unauthorized@chatroom"
	x.request("POST", x.path+"/surf-shares", input, 409)
	input["target"] = "second@chatroom"
	input["job"] = strings.Repeat("a", 64)
	x.request("POST", x.path+"/surf-shares", input, 409)
	input["job"] = job.ID
	x.request("POST", x.path+"/surf-shares", input, 200)
	first, err := x.host.ListReceipts(t.Context(), x.id, "", 1)
	if err != nil || len(first.Items) != 1 || first.NextAfter == "" {
		t.Fatal("surf receipt pagination", first, err)
	}
	second, err := x.host.ListReceipts(t.Context(), x.id, first.NextAfter, 1)
	if err != nil || len(second.Items) != 1 || first.Items[0].IdempotencyKey == second.Items[0].IdempotencyKey {
		t.Fatal("surf page repeated", second, err)
	}
	x.grant(1, "revoke", false)
	input["target"] = "target@chatroom"
	x.request("POST", x.path+"/surf-shares", input, 409)
	receipt, err := x.host.ReadReceipt(t.Context(), x.id, key)
	if err != nil || receipt.State != "accepted" {
		t.Fatal("revocation lost historical acceptance", receipt, err)
	}
	if _, err := x.host.ReadReceipt(t.Context(), wechatapp.ConnectionID("two"), key); err == nil {
		t.Fatal("cross-account receipt read")
	}
}

func TestStationSurfShareUnknownRestartAndPreparedRecovery(t *testing.T) {
	x := newSurfShareFixture(t)
	x.grant(0, "allow", false, "target@chatroom")
	job := x.job("lost")
	x.mu.Lock()
	x.lose = true
	x.hide = true
	x.mu.Unlock()
	input := map[string]string{"job": job.ID, "target": "target@chatroom"}
	unknown := x.request("POST", x.path+"/surf-shares", input, 200)
	key := unknown["idempotency_key"].(string)
	if unknown["state"] != "unknown" {
		t.Fatal("lost reply fabricated success", unknown)
	}
	// Discard the in-memory worker and reopen all private checkpoint files.
	_ = x.web.controller.Close()
	_ = x.web.store.Close()
	x.host.mu.Lock()
	delete(x.host.workers, x.id)
	x.host.mu.Unlock()
	x.request("POST", x.path+"/surf-shares", input, 200)
	x.mu.Lock()
	x.hide = false
	x.lose = false
	x.mu.Unlock()
	r, err := x.host.ReadReceipt(t.Context(), x.id, key)
	if err != nil || r.State != "accepted" || len(x.sends) != 1 {
		t.Fatal("restart resent an unknown operation", r, err, x.sends)
	}
	x.worker, _ = x.host.worker(x.id, false)
	x.web, err = x.host.shareHost(x.worker, "one")
	if err != nil {
		t.Fatal(err)
	}
	preparedJob := x.job("prepared")
	x.mu.Lock()
	x.lookupError = true
	x.mu.Unlock()
	input["job"] = preparedJob.ID
	prepared := x.request("POST", x.path+"/surf-shares", input, 200)
	if prepared["state"] != "prepared" {
		t.Fatal(prepared)
	}
	x.grant(1, "revoke", false)
	x.grant(2, "allow-again", false, "target@chatroom")
	x.mu.Lock()
	x.lookupError = false
	x.mu.Unlock()
	accepted := x.request("POST", x.path+"/surf-shares", input, 200)
	if accepted["state"] != "accepted" || len(x.sends) != 2 {
		t.Fatal("prepared checkpoint stuck after regrant", accepted, x.sends)
	}
}

func TestStationSurfShareInFlightReceiptAndCancel(t *testing.T) {
	x := newSurfShareFixture(t)
	x.grant(0, "allow", false, "target@chatroom")
	job := x.job("in-flight")
	key := surfShareKey("one", "target@chatroom", "https://synthetic.example/in-flight")
	entered, release := make(chan struct{}), make(chan struct{})
	x.mu.Lock()
	x.beforeSubmit = func() { close(entered); <-release }
	x.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := x.host.submitSurfShare(t.Context(), x.id, job.ID, "target@chatroom", "", false, func() error { return nil })
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("submit did not enter")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	r, err := x.host.ReadReceipt(ctx, x.id, key)
	if err != nil || r.State != "unknown" {
		close(release)
		t.Fatal("in-flight receipt blocked", r, err)
	}
	// Stop intent is persisted independently of the submit mutex.
	stopped := make(chan struct{})
	go func() {
		request := httptest.NewRequest("POST", "/", nil)
		out := httptest.NewRecorder()
		x.host.cancelSurfShare(out, request, x.id, key)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("cancel waited for native submit")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	r, err = x.host.ReadReceipt(t.Context(), x.id, key)
	if err != nil || !r.CancelRequested || r.State != "accepted" {
		t.Fatal("accepted result or stop intent lost", r, err)
	}
}

func TestStationSurfShareAutomaticHostReview(t *testing.T) {
	x := newSurfShareFixture(t)
	x.grant(0, "allow-auto", true, "target@chatroom")
	job := x.job("auto")
	if _, err := x.worker.updateSurf("one", stationSurfUpdate{Action: "resume", Revision: 1, Key: "resume", AllowPublicReads: true}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	state, err := x.worker.surfSchedule("one")
	if err != nil {
		t.Fatal(err)
	}
	factory, err := x.host.prepareSurf(t.Context(), x.worker, state, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	access, err := factory(t.Context(), "surf-auto")
	if err != nil {
		t.Fatal(err)
	}
	tools := access.(*stationSurfTools)
	if tools.shareGrant == nil {
		_, _, _, err := x.host.surfShareAuthority(t.Context(), x.worker, "one")
		t.Fatalf("automatic grant was not captured: %v", err)
	}
	ctx, stop, err := tools.BindSurfBrowseContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	input := companion.Input{Channel: "host", Account: "one", Thread: "host:surf:one", TraceID: "surf-auto", Mode: "direct", Preview: true, Surf: true, Text: "合成分享"}
	tools.SetTrace(input)
	config := companion.Defaults()
	config.Enabled = true
	config.QuietStart, config.QuietEnd = "00:00", "00:00"
	config.Permissions = companion.Permissions{ReplyText: true}
	model := &surfShareModel{}
	engine, err := companion.NewPreview(config, model, "合成公开分享")
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools = &surfShareReplayTools{stationSurfTools: tools, job: job}
	engine.MaxToolCalls = 4
	decision, err := engine.Respond(ctx, input)
	if err != nil || decision.Action != "silent" || decision.HostSurfShareState != "accepted" || len(x.sends) != 1 {
		t.Fatal("automatic host review did not produce exactly one accepted link", decision, err, x.sends, "model calls", model.calls, "read", tools.read)
	}
	state, err = x.worker.surfSchedule("one")
	if err != nil || state.LastRun.Share == "" {
		t.Fatal("last run lost receipt", state, err)
	}
	x.grant(1, "revoke", false)
	if out, err := tools.ReviewSurf(ctx, companion.Decision{Action: "reply", SurfItemID: "auto", SurfTargetThread: "target@chatroom"}); err != nil || out.HostSurfShareState != "" || len(x.sends) != 1 {
		t.Fatal("revoked auto grant sent", out, err)
	}
}

func TestStationSurfShareRevocationBoundaries(t *testing.T) {
	x := newSurfShareFixture(t)
	x.grant(0, "allow-auto", true, "target@chatroom")
	job := x.job("boundaries")
	// The automatic path cannot send while its scheduler is paused.
	if _, err := x.host.submitSurfShare(t.Context(), x.id, job.ID, "target@chatroom", "", true, func() error { return nil }); err == nil || len(x.sends) != 0 {
		t.Fatal("paused automatic share was allowed", err)
	}
	// A pre-existing native key without our attempted checkpoint is never
	// proof that the frozen text was delivered, even if the receipt matches.
	key := surfShareKey("one", "target@chatroom", "https://synthetic.example/boundaries")
	x.mu.Lock()
	x.statuses[key] = map[string]any{"id": "synthetic-unowned-send", "accountId": "one", "threadId": "target@chatroom", "kind": "send", "state": "accepted", "serverMessageId": "9911"}
	x.mu.Unlock()
	out, err := x.host.submitSurfShare(t.Context(), x.id, job.ID, "target@chatroom", "", false, func() error { return nil })
	if err != nil || out.State != "unknown" || out.ServerID != "" || len(x.sends) != 0 {
		t.Fatal("unowned native key fabricated acceptance", out, err)
	}
	x.mu.Lock()
	x.dataset = "synthetic-replacement-dataset"
	x.mu.Unlock()
	current := x.request("GET", x.path+"/surf-share-grants", nil, 200)
	if current["current"] != false {
		t.Fatal("dataset replacement left authorization current", current)
	}
	x.request("POST", x.path+"/surf-shares", map[string]string{"job": job.ID, "target": "target@chatroom"}, 409)
	x.mu.Lock()
	x.dataset = "synthetic-surf-dataset"
	x.mu.Unlock()
	// Replacing the reply authorization timestamp invalidates the share grant.
	x.request("PUT", x.path+"/policy", map[string]any{"reply_threads": []string{}}, 200)
	x.request("PUT", x.path+"/policy", map[string]any{"reply_threads": []string{"target@chatroom"}}, 200)
	x.request("POST", x.path+"/surf-shares", map[string]string{"job": job.ID, "target": "target@chatroom"}, 409)
	x.grant(1, "restore", false, "target@chatroom")
	// An unsubmitted record must retain the same source identity; editing the
	// source is not permission to share a checkpoint from its old URL.
	settings := stationSurfDefaults("one").Settings
	settings.Sources = []mcptools.DiscoverySource{x.source}
	settings.Sources[0].URL = "https://synthetic.example/replaced-feed"
	x.request("PUT", x.path+"/surf", stationSurfUpdate{Action: "save", Revision: 1, Settings: &settings, Key: "replace-source"}, 200)
	x.request("POST", x.path+"/surf-shares", map[string]string{"job": job.ID, "target": "target@chatroom"}, 409)
	if len(x.sends) != 0 {
		t.Fatal("revoked source reached native send")
	}
}

func TestStationSurfShareAttemptCannotMoveToReplacementDataset(t *testing.T) {
	x := newSurfShareFixture(t)
	x.grant(0, "allow", false, "target@chatroom")
	job := x.job("dataset")
	key := surfShareKey("one", "target@chatroom", "https://synthetic.example/dataset")
	x.mu.Lock()
	x.lose, x.hide = true, true
	x.mu.Unlock()
	input := map[string]string{"job": job.ID, "target": "target@chatroom"}
	out := x.request("POST", x.path+"/surf-shares", input, 200)
	if out["state"] != "unknown" {
		t.Fatal(out)
	}
	x.mu.Lock()
	x.dataset = "synthetic-replacement-dataset"
	x.lose, x.hide = false, false
	x.statuses[key] = map[string]any{"id": "synthetic-unrelated-send", "accountId": "one", "threadId": "target@chatroom", "kind": "send", "state": "accepted", "serverMessageId": "9981"}
	x.mu.Unlock()
	x.reconnect()
	x.grant(1, "new-dataset", false, "target@chatroom")
	out = x.request("POST", x.path+"/surf-shares", input, 200)
	if out["state"] != "unknown" || out["server_id"] != nil || len(x.sends) != 1 {
		t.Fatal("new dataset claimed old delivery", out, x.sends)
	}
}

// Replays an already verified synthetic read checkpoint through the normal
// runner. The public-reader process itself was verified in the prior stage.
type surfShareReplayTools struct {
	*stationSurfTools
	job pluginhost.Job
}

func (t *surfShareReplayTools) ForkTools() companion.ToolAccess { return t }
func (t *surfShareReplayTools) Call(ctx context.Context, name string, _ json.RawMessage) (string, error) {
	if err := t.check(ctx); err != nil {
		return "", err
	}
	if name == "web_discover" {
		return `{"candidates":[{"id":"auto","title":"合成已读网页","url":"https://synthetic.example/auto"}]}`, nil
	}
	if name != "web_fetch" || t.job.Context.Authorization != t.trace {
		return "", fmt.Errorf("unexpected replay call %s", name)
	}
	raw, err := t.host.store.PrivateJobResult(ctx, t.job, func(ctx context.Context, job pluginhost.Job) error {
		return surfHistorySourceAllowed(ctx, t.worker, t.host, t.account, job)
	})
	if err == nil {
		t.read["auto"], t.readJobs["auto"] = true, t.job
	}
	return string(raw), err
}

type surfShareModel struct{ calls int }

func (m *surfShareModel) Chat(context.Context, string, []map[string]string) (string, error) {
	m.calls++
	if m.calls == 1 {
		return `{"action":"tool","tool":"web_fetch","arguments":{"candidate_id":"auto"}}`, nil
	}
	return `{"action":"reply","messages":["合成分享说明"],"surf_item_id":"auto","surf_target_thread":"target@chatroom"}`, nil
}
