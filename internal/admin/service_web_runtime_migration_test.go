package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func TestWebPublicProxyBindsQueryBackendURLAndRequestedLimits(t *testing.T) {
	_, p, store, job, cap := deepSeekWebJob(t)
	i, err := store.Installed(t.Context(), job.Plugin)
	if err != nil {
		t.Fatal(err)
	}
	i, err = store.SetEnabled(t.Context(), i.Package.ID, i.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := store.Configuration(t.Context(), i.Package.ID)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err = store.SaveConfiguration(t.Context(), i.Package.ID, pluginhost.ConfigUpdate{Revision: configuration.Revision, Values: map[string]json.RawMessage{"search_backend": json.RawMessage(`"public"`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetEnabled(t.Context(), i.Package.ID, i.Revision, true); err != nil {
		t.Fatal(err)
	}
	job.ConfigRevision = strconv.Itoa(configuration.Revision)
	job.Arguments = json.RawMessage(`{"query":"合成 query","max_bytes":196608,"timeout_seconds":45}`)
	calls := 0
	policy := servicePluginPolicy{p: p, observed: map[string]bool{}, read: func(_ context.Context, u string, maximum int64, timeout time.Duration) ([]byte, string, string, error) {
		calls++
		if maximum != 196608 || timeout != 45*time.Second {
			t.Fatal("public proxy changed explicit limits")
		}
		return []byte("synthetic"), "text/plain", u, nil
	}}
	for _, link := range []string{"https://www.bing.com/search?q=%E5%90%88%E6%88%90+query&format=rss", "https://www.so.com/s?q=%E5%90%88%E6%88%90+query"} {
		raw, _ := json.Marshal(map[string]any{"url": link, "max_bytes": 196608, "timeout_seconds": 45})
		if _, err := policy.Service(t.Context(), job, cap, "wechat/http_read", raw); err != nil {
			t.Fatal("configured public search could not read its real index", err)
		}
	}
	for _, link := range []string{"https://example.test/", "https://www.bing.com/search?q=other&format=rss", "http://www.bing.com/search?q=%E5%90%88%E6%88%90+query&format=rss", "https://www.bing.com/search?q=%E5%90%88%E6%88%90+query&format=rss&key=synthetic", "https://www.bing.com:443/search?q=%E5%90%88%E6%88%90+query&format=rss"} {
		raw, _ := json.Marshal(map[string]any{"url": link, "max_bytes": 196608, "timeout_seconds": 45})
		if _, err := policy.Service(t.Context(), job, cap, "wechat/http_read", raw); err == nil {
			t.Fatal("public search changed authorized source", link)
		}
	}
	job.Tool, cap.ID = "web.read", "web.read"
	job.Arguments = json.RawMessage(`{"url":"https://example.test/exact","max_bytes":196608,"timeout_seconds":45}`)
	if _, err := policy.Service(t.Context(), job, cap, "wechat/http_read", json.RawMessage(`{"url":"https://example.test/other","max_bytes":196608,"timeout_seconds":45}`)); err == nil {
		t.Fatal("read changed URL")
	}
	if _, err := policy.Service(t.Context(), job, cap, "wechat/http_read", json.RawMessage(`{"url":"https://example.test/exact","max_bytes":196609,"timeout_seconds":45}`)); err == nil {
		t.Fatal("read enlarged requested byte budget")
	}
	if calls != 2 {
		t.Fatal("rejected sources reached public transport", calls)
	}
}

func TestWebDeepSeekCannotFallBackToPublicHTTP(t *testing.T) {
	_, p, _, job, cap := deepSeekWebJob(t)
	policy := servicePluginPolicy{p: p, observed: map[string]bool{}, read: func(context.Context, string, int64, time.Duration) ([]byte, string, string, error) {
		t.Fatal("selected paid backend reached public fallback")
		return nil, "", "", nil
	}}
	if _, err := policy.Service(t.Context(), job, cap, "wechat/http_read", json.RawMessage(`{"url":"https://www.bing.com/search?q=synthetic+search&format=rss","max_bytes":196608,"timeout_seconds":45}`)); err == nil {
		t.Fatal("deepseek selection accepted public HTTP")
	}
}

func TestWebPublicReadDropsRevokedResponseAndCancelledContext(t *testing.T) {
	for _, action := range []string{"cancel", "disable", "forget", "task_cancel", "drain"} {
		t.Run(action, func(t *testing.T) {
			f, p, store, job, cap := deepSeekWebJob(t)
			job.Tool, cap.ID = "web.read", "web.read"
			job.Arguments = json.RawMessage(`{"url":"https://example.test/exact"}`)
			input := p.trace
			input.Text = "synthetic public web request"
			if err := f.manager.memory.Observe(input, f.manager.config.Companion, time.Now()); err != nil {
				t.Fatal(err)
			}
			job.Context.EvidenceEpoch, _ = f.manager.memory.RecallEpoch(t.Context())
			if action == "task_cancel" {
				created, _, err := store.CreateJob(t.Context(), job)
				if err != nil {
					t.Fatal(err)
				}
				job, err = store.ClaimJob(t.Context(), created.ID, created.Revision)
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			policy := servicePluginPolicy{p: p, observed: map[string]bool{}, read: func(context.Context, string, int64, time.Duration) ([]byte, string, string, error) {
				switch action {
				case "cancel":
					cancel()
				case "disable":
					i, err := store.Installed(t.Context(), job.Plugin)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = store.SetEnabled(t.Context(), i.Package.ID, i.Revision, false); err != nil {
						t.Fatal(err)
					}
				case "forget":
					if err := f.manager.memory.Clear(companion.ScopeID("wechat", p.trace.Account, p.trace.Thread)); err != nil {
						t.Fatal(err)
					}
				case "task_cancel":
					if _, err := store.RequestJobCancel(t.Context(), job.ID, job.Revision); err != nil {
						t.Fatal(err)
					}
				case "drain":
					f.manager.drainTicket = "synthetic-drain"
				}
				return []byte("synthetic private result must be discarded"), "text/plain", "https://example.test/exact", nil
			}}
			data, err := policy.Service(ctx, job, cap, "wechat/http_read", json.RawMessage(`{"url":"https://example.test/exact","max_bytes":2097152,"timeout_seconds":20}`))
			if err == nil || len(data) != 0 || len(policy.observed) != 0 {
				t.Fatal("revoked response became observed data", action, string(data), err)
			}
		})
	}
}

func configureSyntheticSurf(t *testing.T, f *webFixture, p *permittedTools) mcptools.DiscoverySource {
	t.Helper()
	source := mcptools.DiscoverySource{ID: "safe-rss", Name: "合成订阅", Type: "rss", URL: "https://example.test/feed", Kind: "image", Enabled: true}
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.Proactive = true
	f.manager.config.Companion.QuietStart, f.manager.config.Companion.QuietEnd = "00:00", "00:00"
	f.manager.surf.Settings.Enabled = true
	f.manager.surf.Settings.Threads = []string{p.trace.Thread}
	f.manager.surf.Settings.Discovery.Sources = []mcptools.DiscoverySource{source}
	p.trace.Surf, p.trace.Sender, p.trace.TraceID = true, "", "surf-synthetic-package"
	return source
}

func TestWebDiscoveryHostBindsConfiguredSourceAndMetadata(t *testing.T) {
	f, p := trustedAgent(t)
	installPublicWebFixture(t, f)
	source := configureSyntheticSurf(t, f, p)
	args, _ := json.Marshal(webDiscoveryArguments{Source: source})
	if _, link, err := p.webDiscoveryRequest(args); err != nil || link != source.URL {
		t.Fatal("current source rejected", link, err)
	}
	result := serviceWebResult{OK: true, Untrusted: true, MetadataOnly: true, Source: source.URL, RequestedLimit: 40, TotalItems: 1, Complete: true, Candidates: []mcptools.DiscoveryItem{{Title: "合成候选", URL: "https://example.test/item", MediaURL: "https://example.test/image.png", Kind: "image", Source: source.Name}}}
	observed := map[string]bool{source.URL: true}
	if err := p.validateWebDiscoveryResult(args, result, observed); err != nil {
		t.Fatal(err)
	}
	result.Text = "invented watched-video observation"
	if p.validateWebDiscoveryResult(args, result, observed) == nil {
		t.Fatal("metadata presented as content observation")
	}
	result.Text = ""
	if p.validateWebDiscoveryResult(args, result, nil) == nil {
		t.Fatal("unobserved source accepted")
	}
	f.manager.surf.Settings.Discovery.Sources[0].URL = "https://example.test/replaced"
	if _, _, err := p.webDiscoveryRequest(args); err == nil {
		t.Fatal("replaced subscription authorized old result")
	}
	if p.validateWebDiscoveryResult(args, result, observed) == nil {
		t.Fatal("replaced source exposed old candidates")
	}
}

// Uses the official package and actual local Windows LPAC/MCP runtime. HTTP
// replies are synthetic in a host-owned test proxy; no live network or sends.
func TestLiveWebPublicMigrationIsolationContract(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in isolated public web contract, synthetic HTTP only")
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	f, p := trustedAgent(t)
	store, installed := installPublicWebFixture(t, f)
	helper, err := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	if err := controller.Start(t.Context(), installed.Package.ID, installed.Revision); err != nil {
		t.Fatal("official web runtime startup failed", err)
	}
	p.hub = nil // a public alias must never reach the old Hub path.
	calls := 0
	p.webPageReader = func(_ context.Context, link string, maximum int64, timeout time.Duration) ([]byte, string, string, error) {
		calls++
		if strings.Contains(link, "bing.com/search") {
			return []byte(`<rss><channel><item><title>合成搜索</title><link>https://example.test/article</link><description>合成摘要</description></item></channel></rss>`), "text/xml", link, nil
		}
		if link == "https://example.test/article" {
			return []byte(`<title>合成标题</title><script>not an instruction</script><p>合成正文</p>`), "text/html", link, nil
		}
		if link == "https://example.test/feed" {
			return []byte(`<rss><channel><item><title>合成图片</title><link>https://example.test/item</link><description>合成简介</description><enclosure url="https://example.test/image.png" type="image/png"/></item></channel></rss>`), "application/xml", link, nil
		}
		return nil, "", "", errors.New("unexpected synthetic source")
	}
	search, err := p.Call(t.Context(), "web_search", json.RawMessage(`{"query":"合成","count":9,"limit":1}`))
	if err != nil || !strings.Contains(search, "合成搜索") || calls != 1 {
		t.Fatal("actual public search could not use its authorized GET", search, calls, err)
	}
	if _, err := p.Call(t.Context(), "web_search", json.RawMessage(`{"query":"合成","count":9,"limit":1}`)); err != nil || calls != 1 {
		t.Fatal("same source/key repeated HTTP instead of recovering", calls, err)
	}
	read, err := p.Call(t.Context(), "web_fetch", json.RawMessage(`{"url":"https://example.test/article","limit":3}`))
	if err != nil || !strings.Contains(read, "合成正") || strings.Contains(read, "not an instruction") || calls != 2 {
		t.Fatal("actual page parser failed", read, calls, err)
	}
	configureSyntheticSurf(t, f, p)
	discovered, err := p.Call(t.Context(), "web_discover", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(discovered, "https://example.test/image.png") || calls != 3 {
		t.Fatal("actual plugin discovery lost media candidates", discovered, calls, err)
	}
	if len(f.manager.surfHistory.snapshot().Items) != 1 {
		t.Fatal("plugin candidates not recorded for novelty")
	}
	discovered, err = p.Call(t.Context(), "web_discover", json.RawMessage(`{}`))
	if err != nil || calls != 3 || bytes.Contains([]byte(discovered), []byte(`"candidates":[{`)) {
		t.Fatal("same plugin candidates duplicated or re-fetched", discovered, calls, err)
	}
}
