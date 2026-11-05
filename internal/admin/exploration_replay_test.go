package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginruntime"
)

// explorationReplayScript is a local function-calling model substitute.  It
// deliberately performs only discovery and then ends the autonomous round;
// its captured turns prove exactly what the production context assembler gave
// a model, without any provider call.
type explorationReplayScript struct {
	t     *testing.T
	calls int
	turns [][]map[string]string
}

func (m *explorationReplayScript) Chat(context.Context, string, []map[string]string) (string, error) {
	m.t.Fatal("exploration replay must use the local function-calling path")
	return "", nil
}

func (m *explorationReplayScript) ChatFunctions(_ context.Context, _ string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
	copyTurns := make([]map[string]string, len(turns))
	for i, turn := range turns {
		copyTurns[i] = map[string]string{}
		for key, value := range turn {
			copyTurns[i][key] = value
		}
	}
	m.turns = append(m.turns, copyTurns)
	m.calls++
	if m.calls == 1 {
		return `{"action":"tool","tool":"discover_tools","arguments":{"query":"web_discover"}}`, nil
	}
	if m.calls == 2 {
		found := false
		for _, tool := range tools {
			found = found || tool.Name == "web_discover"
		}
		if !found {
			m.t.Fatal("discovered surf schema was not returned to the model")
		}
		return `{"action":"tool","tool":"web_discover","arguments":{}}`, nil
	}
	return `{"action":"silent","messages":[],"reason":"surf_not_interested"}`, nil
}

// TestExplorationReplayAcrossWebAndOfflinePlatform exercises the full 4.3
// story without a public endpoint, model, or WeChat send.  The first source
// goes through the installed web-search package and an isolated HTTP reader;
// the second is an independently injected platform adapter.  Both must leave
// the same source-scoped, revalidated reading evidence for a later exploration
// of the same interest.
func TestExplorationReplayAcrossWebAndOfflinePlatform(t *testing.T) {
	python := strings.TrimSpace(os.Getenv("WECHAT_PLUGIN_TEST_PYTHON"))
	if python == "" {
		t.Skip("opt-in isolated Windows web-plugin replay; set WECHAT_PLUGIN_TEST_PYTHON")
	}
	var err error
	if python, err = filepath.Abs(python); err != nil {
		t.Fatal(err)
	}

	f, p := trustedAgent(t)
	store, installed := installPublicWebFixture(t, f)
	helper, err := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, pluginruntime.Options{
		Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"),
		WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	if err := controller.Start(t.Context(), installed.Package.ID, installed.Revision); err != nil {
		t.Fatal("start isolated web adapter", err)
	}
	p.hub = nil // The compatibility aliases must not reach a legacy provider.

	const interest = "海岸潮汐与蓝色夜空"
	webSource := mcptools.DiscoverySource{
		ID: "replay-web", Name: "隔离网页", Type: "rss", URL: "https://example.test/replay-web-feed", Kind: "text", Enabled: true,
	}
	offlineSource := mcptools.DiscoverySource{
		ID: "replay-offline", Name: "离线模拟平台", Type: "rss", URL: "https://example.test/replay-offline-feed", Kind: "image", Enabled: true,
	}
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.Proactive = true
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.QuietStart, f.manager.config.Companion.QuietEnd = "00:00", "00:00"
	f.manager.surf.Settings.Enabled = true
	f.manager.surf.Settings.Threads = []string{p.trace.Thread}
	// Both adapters are authorized at the beginning.  MaxSources deliberately
	// permits one source per round; the requested kind chooses which source is
	// eligible, rather than mutating settings between histories and bypassing
	// the source revision/authorization boundary.
	f.manager.surf.Settings.Discovery.Sources = []mcptools.DiscoverySource{webSource, offlineSource}
	f.manager.surf.Settings.Discovery.MaxSources = 1

	// This is the isolated HTTP boundary used by the official web-search
	// package.  No live network request can leave the test process.
	p.webPageReader = func(_ context.Context, link string, _ int64, _ time.Duration) ([]byte, string, string, error) {
		switch link {
		case webSource.URL:
			if p.trace.TraceID == "replay-engine" {
				return []byte(`<rss><channel><item><title>网页发现：潮汐灯塔</title><link>https://example.test/replay-engine</link><description>新的蓝色海岸标题</description></item></channel></rss>`), "application/xml", link, nil
			}
			return []byte(`<rss><channel><item><title>网页发现：蓝色潮汐</title><link>https://example.test/replay-web</link><description>海岸边的蓝色潮汐</description></item></channel></rss>`), "application/xml", link, nil
		case "https://example.test/replay-web":
			return []byte(`<title>蓝色潮汐</title><p>网页正文：潮水在夜色里泛着蓝光。</p>`), "text/html", link, nil
		case "https://example.test/replay-offline":
			return []byte(`<title>离线潮汐笔记</title><p>离线正文：海风把潮声带到礁石边。</p>`), "text/html", link, nil
		default:
			return nil, "", "", errors.New("unexpected isolated replay URL: " + link)
		}
	}

	setReplayTrace := func(id, preferredKind string) {
		p.SetTrace(companion.Input{
			Channel: "wechat", Account: "test", Thread: "wxid_owner", Mode: "proactive",
			TraceID: id, Surf: true, SurfTopic: interest, SurfPreferredKind: preferredKind,
		})
	}
	discover := func() mcptools.DiscoveryItem {
		t.Helper()
		raw, err := p.Call(t.Context(), "web_discover", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal("discover", err)
		}
		var out struct {
			Candidates []mcptools.DiscoveryItem `json:"candidates"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil || len(out.Candidates) != 1 {
			t.Fatalf("unexpected discovery result: %s (%v)", raw, err)
		}
		return out.Candidates[0]
	}
	readAndStop := func(item mcptools.DiscoveryItem) {
		t.Helper()
		args, _ := json.Marshal(map[string]any{"url": item.URL, "limit": 512})
		if raw, err := p.Call(t.Context(), "web_fetch", args); err != nil || !strings.Contains(raw, "正文") {
			t.Fatalf("read %s: %v; %s", item.URL, err, raw)
		}
		decision, err := p.ReviewSurf(t.Context(), companion.Decision{Action: "silent", Reason: "surf_not_interested", SurfItemID: item.ID})
		if err != nil || decision.Action != "silent" || decision.Reason != "surf_not_interested" {
			t.Fatalf("end exploration: %+v (%v)", decision, err)
		}
	}

	// The injected adapter is a narrow platform router, not a replacement for
	// the actual web source: when the selected source is the web one, it calls
	// readInstalledDiscovery and therefore enters the installed package.
	p.discoveryReader = func(ctx context.Context, source mcptools.DiscoverySource, related string) ([]mcptools.DiscoveryItem, error) {
		switch source.ID {
		case webSource.ID:
			return p.readInstalledDiscovery(ctx, source, related)
		case offlineSource.ID:
			if source.Name != offlineSource.Name || source.URL != offlineSource.URL {
				return nil, errors.New("offline adapter received a changed source")
			}
			if p.trace.TraceID == "replay-title-only" {
				return []mcptools.DiscoveryItem{{Title: "离线标题：潮汐预报", Summary: "仅供决定是否继续读取", URL: "https://example.test/replay-title-only", Kind: "image", Source: source.Name}}, nil
			}
			return []mcptools.DiscoveryItem{{Title: "离线发现：蓝色潮汐", Summary: "同一兴趣的独立模拟来源", URL: "https://example.test/replay-offline", Kind: "image", Source: source.Name}}, nil
		default:
			return nil, errors.New("unknown replay platform")
		}
	}

	// 1. The public-web half uses the installed adapter for both discovery and
	// reading.  The text preference selects it from the two pre-authorized
	// sources without changing settings.
	setReplayTrace("replay-web", "text")
	webItem := discover()
	if webItem.Source != webSource.Name || !strings.Contains(webItem.Title, "蓝色潮汐") {
		t.Fatalf("web candidate lost its source/title: %+v", webItem)
	}
	readAndStop(webItem)

	// 2. A separate platform owns only its candidate discovery.  The image
	// preference selects it, while body reading remains the exact installed
	// web-read path above, preserving one common evidence lifecycle.
	setReplayTrace("replay-offline", "image")
	offlineItem := discover()
	if offlineItem.Source != offlineSource.Name || !strings.Contains(offlineItem.Title, "离线发现") {
		t.Fatalf("offline candidate did not use the independent adapter: %+v", offlineItem)
	}
	readAndStop(offlineItem)

	// 3. Seeing only a title can be a complete choice: it produces a durable
	// stopped lifecycle, but no reading observation and nothing eligible for a
	// future content recap.
	setReplayTrace("replay-title-only", "image")
	titleOnly := discover()
	if titleOnly.Source != offlineSource.Name || !strings.Contains(titleOnly.Summary, "仅供决定") {
		t.Fatalf("title-only choice did not stay on the offline platform: %+v", titleOnly)
	}
	decision, err := p.ReviewSurf(t.Context(), companion.Decision{Action: "silent", Reason: "surf_not_interested", SurfItemID: titleOnly.ID})
	if err != nil || decision.Action != "silent" || decision.Reason != "surf_not_interested" {
		t.Fatalf("title-only exploration did not end immediately: %+v (%v)", decision, err)
	}

	history := f.manager.surfHistory.snapshot()
	if len(history.Sessions) != 3 {
		t.Fatalf("want three independent exploration lifecycles, got %+v", history.Sessions)
	}
	for _, session := range history.Sessions[:2] {
		if session.Account != "test" || session.Thread != "wxid_owner" || session.Interest != interest || session.ScopeKey == "" || session.InterestKey == "" || session.StoppedAt.IsZero() {
			t.Fatalf("incomplete scoped lifecycle: %+v", session)
		}
		if len(session.Observations) != 1 || session.Observations[0].State != "body_read" || session.Observations[0].Platform == "" {
			t.Fatalf("read lifecycle was not preserved: %+v", session.Observations)
		}
	}
	if history.Sessions[0].InterestKey != history.Sessions[1].InterestKey || history.Sessions[0].ScopeKey != history.Sessions[1].ScopeKey {
		t.Fatalf("same interest/scope did not remain replayable: %+v", history.Sessions)
	}
	titleSession := history.Sessions[2]
	if titleSession.StoppedAt.IsZero() || len(titleSession.Observations) != 0 || titleSession.InterestKey != history.Sessions[0].InterestKey || titleSession.ScopeKey != history.Sessions[0].ScopeKey {
		t.Fatalf("title-only decision polluted read lifecycle: %+v", titleSession)
	}

	// 4. A later pass in the same authorized account, conversation and interest
	// sees only verified body excerpts from the two prior sessions.  It gets no
	// candidate metadata, failed reads, private origin material, or send state.
	setReplayTrace("replay-revisit", "text")
	raw, err := p.Call(t.Context(), "web_discover", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal("revisit discovery", err)
	}
	var revisit struct {
		Prior []surfReadingExcerpt `json:"prior_authorized_reading_excerpts"`
	}
	if err := json.Unmarshal([]byte(raw), &revisit); err != nil || len(revisit.Prior) != 2 {
		t.Fatalf("scoped prior readings unavailable: %s (%v)", raw, err)
	}
	seen := map[string]bool{}
	for _, row := range revisit.Prior {
		seen[row.URL] = true
		if row.Kind != companion.ObservationPageBody || row.Excerpt == "" || strings.Contains(row.Excerpt, "发现：") {
			t.Fatalf("revisit exposed non-reading evidence: %+v", row)
		}
	}
	if !seen["https://example.test/replay-web"] || !seen["https://example.test/replay-offline"] {
		t.Fatalf("revisit crossed platform boundary or lost evidence: %+v", revisit.Prior)
	}
	for _, row := range revisit.Prior {
		if row.Platform != webSource.Name && row.Platform != offlineSource.Name {
			t.Fatalf("revisit omitted the verified platform provenance: %+v", row)
		}
	}

	// 5. Feed that same replay through the actual companion.Engine.  The
	// scripted local model discovers the schema, asks for the current
	// exploration, receives the two prior platform-labelled excerpts, and ends
	// immediately with surf_not_interested.  Nothing here calls a paid model.
	f.manager.config.Companion.Permissions.ReplyText = true
	model := &explorationReplayScript{t: t}
	engine, err := companion.New(f.manager.config.Companion, f.manager.memory, model, "isolated replay persona")
	if err != nil {
		t.Fatal("build replay engine", err)
	}
	engine.Tools, engine.MaxToolCalls = p, 4
	engineInput := companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Mode: "proactive", TraceID: "replay-engine", Surf: true, SurfTopic: interest, SurfPreferredKind: "text"}
	engineDecision, err := engine.Respond(t.Context(), engineInput)
	if err != nil || engineDecision.Action != "silent" || engineDecision.Reason != "surf_not_interested" {
		t.Fatalf("engine could not end immediately after discovery: %+v (%v)", engineDecision, err)
	}
	if model.calls != 3 {
		t.Fatalf("unexpected model loop count: %d", model.calls)
	}
	modelInput := ""
	for _, batch := range model.turns {
		for _, turn := range batch {
			modelInput += "\n" + turn["content"]
		}
	}
	for _, required := range []string{"prior_authorized_reading_excerpts", `"platform":"隔离网页"`, `"platform":"离线模拟平台"`, "网页正文：潮水", "离线正文：海风"} {
		if !strings.Contains(modelInput, required) {
			t.Fatalf("model never received required prior reading evidence %q: %s", required, modelInput)
		}
	}
	for _, forbidden := range []string{"replay-title-only", "离线标题：潮汐预报"} {
		if strings.Contains(modelInput, forbidden) {
			t.Fatalf("title-only candidate became model reading context: %q", forbidden)
		}
	}

	// fakeNative deliberately panics if any test path sends.  Check the durable
	// journal too so a future queue implementation cannot turn this replay into
	// a hidden external delivery.
	f.manager.mu.Lock()
	defer f.manager.mu.Unlock()
	for _, entry := range f.manager.logs {
		if entry.Event == "message_sent" || entry.Category == "sent" {
			t.Fatalf("exploration replay must never send: %+v", entry)
		}
	}
}
