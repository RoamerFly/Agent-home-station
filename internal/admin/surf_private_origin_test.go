package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginruntime"
)

func privateSurfFixture(t *testing.T) (*webFixture, *bot.SurfTask, *privateSurfTools) {
	t.Helper()
	f := setup(t)
	installPublicWebFixture(t, f)
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.QuietStart, f.manager.config.Companion.QuietEnd = "00:00", "00:00"
	// The independent browsing authorization does not imply any sending grant.
	f.manager.config.Companion.Permissions = companion.Permissions{Remember: true}
	f.manager.surf.Settings.Enabled = true
	f.manager.surf.Settings.ForceShare = true
	f.manager.surf.Settings.Threads = nil
	f.manager.surf.Settings.Discovery.Sources = []mcptools.DiscoverySource{{ID: "private-rss", Name: "合成浏览订阅", Type: "rss", URL: "https://example.test/feed", Kind: "text", Enabled: true}}
	a := f.manager.agent.config()
	a.Enabled = false
	if err := f.manager.agent.save(a); err != nil {
		t.Fatal(err)
	}
	task, err := f.manager.claimSurf(time.Now())
	if err != nil || task == nil || task.Thread != "" || task.ForceShare || task.PrivateBrowseTools == nil {
		t.Fatal("private claim still requires send permissions", task, err)
	}
	tools, err := task.PrivateBrowseTools(t.Context(), "surf-private-synthetic")
	if err != nil {
		t.Fatal(err)
	}
	return f, task, tools.(*privateSurfTools)
}

func TestPrivateSurfOriginOnlyGrantsReadonlyHostBrowsing(t *testing.T) {
	f, task, tools := privateSurfFixture(t)
	p := tools.tools
	p.hub = nil // discovery must not touch arbitrary configured external servers.
	catalog, err := tools.List(t.Context())
	if err != nil || len(catalog) != 3 {
		t.Fatal("readonly browse catalog", catalog, err)
	}
	for _, tool := range catalog {
		if !privateSurfTool(tool.Name) {
			t.Fatal("extra capability", tool.Name)
		}
	}
	for _, name := range []string{"service_plugins", "service_plugin_call", "service_plugin_task", "web_audio_search", "web_media_download", "media_recent", "media_reuse", "memory_read", "memory_search", "agent_global_search", "server_read", "wechat_send", "device_observe", "evil__tool", "tool_output_read", "speech_synthesize"} {
		if out, err := tools.Call(t.Context(), name, json.RawMessage(`{}`)); err == nil || out != "" {
			t.Fatal("private browse escaped whitelist", name, out, err)
		}
	}
	ordinary, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	ordinary.SetTrace(p.trace)
	if ordinary.servicePluginsAllowed() || ordinary.enabled("web_fetch") {
		t.Fatal("ordinary Preview forged private browse authority")
	}
	if _, err := task.PrivateBrowseTools(t.Context(), p.trace.TraceID); err == nil {
		t.Fatal("claim factory issued twice")
	}
	encoded, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var copied bot.SurfTask
	if err := json.Unmarshal(encoded, &copied); err != nil || copied.PrivateBrowseTools != nil || strings.Contains(string(encoded), p.privateSurfOrigin.claim) {
		t.Fatal("serialized claim retained execution authority", string(encoded), err)
	}
	loaded, err := loadSurf(f.dir)
	if err != nil || loaded.Sessions != 1 || len(loaded.Settings.Threads) != 0 {
		t.Fatal("private claim ledger lost", err)
	}
	f.manager.surf = loaded
	if next, err := f.manager.claimSurf(time.Now()); err != nil || next != nil {
		t.Fatal("restarted schedule blindly replayed claim", next, err)
	}
	f.login()
	code, body, _ := f.request("GET", "/api/surf", nil, false, false)
	var view struct {
		Ready  bool   `json:"execution_ready"`
		Reason string `json:"execution_reason"`
	}
	if code != 200 || json.Unmarshal(body, &view) != nil || !view.Ready || view.Reason != "" {
		t.Fatal("empty targets still shown blocked", code, string(body))
	}
}

func TestPrivateSurfOriginRejectsTransferAndRevocation(t *testing.T) {
	for _, change := range []string{"account", "trace", "thread", "sender", "topic", "source", "config_revision", "settings", "disabled", "drain", "quiet", "expiry", "execution_cancel"} {
		t.Run(change, func(t *testing.T) {
			f, _, tools := privateSurfFixture(t)
			p := tools.tools
			ctx, stop, err := tools.BindSurfBrowseContext(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			switch change {
			case "account":
				p.trace.Account = "other"
			case "trace":
				p.trace.TraceID = "surf-another"
			case "thread":
				p.trace.Thread = "filehelper"
			case "topic":
				p.trace.SurfTopic = "another interest"
			case "sender":
				p.trace.Sender = "wxid_owner"
			case "source":
				p.trace.Source = &companion.SourceRef{MessageID: "invented"}
			case "config_revision":
				f.login()
				before := f.manager.surf.SettingsRevision
				if code, _, _ := f.request("POST", "/api/surf", f.manager.surf.Settings, true, true); code != 200 || f.manager.surf.SettingsRevision != before+1 {
					t.Fatal("same-config save did not revoke old revision", code)
				}
			case "settings":
				f.manager.surf.Settings.Topics = []string{"changed"}
			case "disabled":
				f.manager.surf.Settings.Enabled = false
			case "drain":
				f.manager.drainTicket = "synthetic-drain"
			case "quiet":
				f.manager.config.Companion.QuietStart = "00:00"
				f.manager.config.Companion.QuietEnd = "23:59"
			case "expiry":
				p.privateSurfOrigin.expires = time.Now().Add(-time.Second)
			case "execution_cancel":
				stop()
				ctx = context.Background()
			}
			if _, err := tools.List(ctx); err == nil {
				t.Fatal("revoked private browse still discoverable", change)
			}
			if out, err := tools.Call(ctx, "web_fetch", json.RawMessage(`{"url":"https://must-not-resolve.invalid/"}`)); err == nil || out != "" {
				t.Fatal("revoked private browse reached execution", out, err)
			}
		})
	}
}

type privateSurfModel struct {
	t     *testing.T
	calls int
	item  string
}

func (m *privateSurfModel) Chat(_ context.Context, _ string, turns []map[string]string) (string, error) {
	m.calls++
	for _, turn := range turns {
		if strings.Contains(turn["content"], "PRIVATE-CONVERSATION-SENTINEL") {
			m.t.Fatal("private browse received conversation memory")
		}
	}
	if m.calls == 1 {
		return `{"action":"tool","tool":"web_fetch","arguments":{"url":"https://example.test/article","limit":10000}}`, nil
	}
	return `{"action":"silent","reason":"surf_browse_only","surf_action":"bookmark","surf_item_id":"` + m.item + `"}`, nil
}

// Runs the actual installed web plugin in the local isolation runtime. Only
// the host's public HTTP fetch is synthetic; no provider/device/WeChat I/O.
func TestLivePrivateSurfReadsInstalledWebWithoutChatOrSendAuthority(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in LPAC contract with synthetic HTTP")
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	f, _, tools := privateSurfFixture(t)
	p := tools.tools
	p.hub = nil
	private := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "synthetic-person", TraceID: "private-only", Text: "PRIVATE-CONVERSATION-SENTINEL", Mode: "direct"}
	if err := f.manager.memory.Observe(private, f.manager.config.Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	before := f.manager.memory.Context(private, f.manager.config.Companion, time.Now())
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	controller, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	i, err := store.Installed(t.Context(), "web-search")
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(t.Context(), i.Package.ID, i.Revision); err != nil {
		t.Fatal(err)
	}
	reads := 0
	p.webPageReader = func(_ context.Context, link string, _ int64, _ time.Duration) ([]byte, string, string, error) {
		reads++
		if link == "https://example.test/feed" {
			return []byte(`<rss><channel><item><title>合成浏览文章</title><link>https://example.test/article</link><description>真实合成简介</description></item></channel></rss>`), "text/xml", link, nil
		}
		if link == "https://example.test/article" {
			return []byte(`<title>合成标题</title><p>合成正文，实际读取。</p>`), "text/html", link, nil
		}
		t.Fatal("unexpected synthetic HTTP source", link)
		return nil, "", "", nil
	}
	ctx, stop, err := tools.BindSurfBrowseContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	item := surfIdentity(mcptools.DiscoveryItem{URL: "https://example.test/article", Kind: "text"})
	model := &privateSurfModel{t: t, item: item}
	base, err := companion.New(f.manager.config.Companion, f.manager.memory, model, "synthetic system")
	if err != nil {
		t.Fatal(err)
	}
	engine, err := base.IsolatedPreview()
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools = tools
	engine.Config.Permissions = companion.Permissions{ReplyText: true}
	engine.MaxToolCalls = 4
	d, err := engine.Respond(ctx, p.trace)
	if err != nil || d.Action != "silent" || len(d.MediaIDs) != 0 || reads != 2 {
		t.Fatal("private plugin browser did not complete actual reads", d, reads, err)
	}
	history := f.manager.surfHistory.snapshot()
	if len(history.Items) != 1 || !history.Items[0].Bookmarked || len(history.Sessions) != 1 || len(history.Sessions[0].Visited) < 1 {
		t.Fatal("private actual reading/bookmark not retained", history)
	}
	if after := f.manager.memory.Context(private, f.manager.config.Companion, time.Now()); !reflect.DeepEqual(before, after) {
		t.Fatal("private browser modified real conversation memory")
	}
	jobs, err := store.Jobs(t.Context(), "web-search", 0, 10)
	if err != nil || len(jobs.Items) != 2 {
		t.Fatal("private jobs missing", jobs, err)
	}
	for _, job := range jobs.Items {
		if job.Context.Thread != "host:surf:test" || job.Context.Requester != "host:surf" || job.Context.Purpose != "private autonomous public web browsing" || job.State != "completed" {
			t.Fatal("private job forged conversation identity", job)
		}
	}
	stop()
	if _, err := tools.List(context.Background()); err == nil {
		t.Fatal("completed origin replayed after its execution context was cancelled")
	}
	// Cancellation during the real plugin's host-read callback must discard
	// the response and must not mark it as visited in private surf history.
	f.manager.surf.Next = time.Time{}
	task, err := f.manager.claimSurf(time.Now())
	if err != nil || task == nil {
		t.Fatal(err)
	}
	parent, cancelOrigin := context.WithCancel(t.Context())
	defer cancelOrigin()
	next, err := task.PrivateBrowseTools(parent, "surf-private-cancelled")
	if err != nil {
		t.Fatal(err)
	}
	nextTools := next.(*privateSurfTools)
	nextTools.tools.webPageReader = func(_ context.Context, link string, _ int64, _ time.Duration) ([]byte, string, string, error) {
		cancelOrigin()
		return []byte("MUST-NOT-SURVIVE"), "text/plain", link, nil
	}
	result, err := nextTools.Call(t.Context(), "web_fetch", json.RawMessage(`{"url":"https://example.test/article"}`))
	if err == nil || strings.Contains(result, "MUST-NOT-SURVIVE") {
		t.Fatal("cancelled private origin exposed response", result, err)
	}
	if len(f.manager.surfHistory.snapshot().Sessions) != 1 {
		t.Fatal("cancelled origin wrote a browsing session")
	}
}
