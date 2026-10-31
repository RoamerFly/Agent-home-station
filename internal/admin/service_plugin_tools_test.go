package admin

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func deepSeekWebJob(t *testing.T) (*webFixture, *permittedTools, *pluginhost.Store, pluginhost.Job, pluginhost.Capability) {
	t.Helper()
	f, p := trustedAgent(t)
	ctx := context.Background()
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(ctx, "../../plugins/web-search")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(ctx, prepared.Token, prepared.Digest, []string{"network"})
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.SaveConfiguration(ctx, installed.Package.ID, pluginhost.ConfigUpdate{
		Revision: 0,
		Values:   map[string]json.RawMessage{"search_backend": json.RawMessage(`"deepseek"`)},
		Secrets:  map[string]string{"search_key": "synthetic-search-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	installed, err = store.SetEnabled(ctx, installed.Package.ID, installed.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	caps, err := store.Capabilities(ctx, installed.Package.ID)
	if err != nil {
		t.Fatal(err)
	}
	var cap pluginhost.Capability
	for _, item := range caps {
		if item.ID == "web.search" {
			cap = item
		}
	}
	if cap.ID == "" {
		t.Fatal("web.search capability missing")
	}
	epoch, err := f.manager.memory.RecallEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	job := pluginhost.Job{
		ID:             "deepseek-synthetic",
		Plugin:         installed.Package.ID,
		Version:        installed.Package.Version,
		PackageDigest:  installed.Digest,
		ConfigRevision: strconv.Itoa(config.Revision),
		Tool:           "web.search",
		Arguments:      json.RawMessage(`{"query":"  synthetic search  ","count":9,"limit":1,"max_bytes":196608,"timeout_seconds":45}`),
		Context:        pluginhost.Invocation{Account: p.trace.Account, Thread: p.trace.Thread, Requester: p.trace.Sender, Purpose: "synthetic", RequestKey: "deepseek-one", EvidenceEpoch: epoch, Authorization: p.serviceAuthorization()},
	}
	return f, p, store, job, cap
}

func TestServiceDeepSeekRouteBindsProviderAndResult(t *testing.T) {
	_, p, _, job, cap := deepSeekWebJob(t)
	ctx := context.Background()
	calls := 0
	policy := servicePluginPolicy{p: p, observed: map[string]bool{}, deepseek: map[string]deepSeekSearchEvidence{}, post: func(_ context.Context, endpoint string, headers map[string]string, body json.RawMessage, maxBytes int64, timeout time.Duration) (json.RawMessage, error) {
		calls++
		if endpoint != "https://api.deepseek.com/v1/tools/search" || headers["Authorization"] != "Bearer synthetic-search-key" || maxBytes != 196608 || timeout != 45*time.Second || string(body) != `{"query":"synthetic search","count":9,"need_content":false}` {
			t.Fatalf("unexpected route request: endpoint=%q headers=%v body=%s max=%d timeout=%s", endpoint, headers, body, maxBytes, timeout)
		}
		return json.RawMessage(`{"results":[{"title":"  Synthetic  ","url":"https://example.test/a","summary":"  first result "},{"title":"Other","url":"https://example.test/b","snippet":"second"}]}`), nil
	}}
	route := json.RawMessage(`{"route":"deepseek-search","body":{"query":"synthetic search","count":9,"need_content":false},"max_bytes":196608,"timeout_seconds":45}`)
	if _, err := policy.Service(ctx, job, cap, "wechat/http_route", route); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d", calls)
	}
	pluginResult := json.RawMessage(`{"ok":true,"backend":"deepseek","query":"  synthetic search  ","items":[{"url":"https://example.test/a","title":"Synthetic","summary":"first result"}],"fallback_used":false,"requested_count":9,"effective_provider_count":9,"requested_limit":1,"provider_items":2,"next_offset":1,"provider_page_exhausted":false,"untrusted_external_content":true}`)
	if _, err := policy.Validate(ctx, job, cap, pluginResult); err != nil {
		t.Fatal(err)
	}
	forged := json.RawMessage(`{"ok":true,"backend":"deepseek","query":"  synthetic search  ","items":[{"url":"https://attacker.test/a","title":"Synthetic","summary":"first result"}],"fallback_used":false,"requested_count":9,"effective_provider_count":9,"requested_limit":1,"provider_items":2,"next_offset":1,"provider_page_exhausted":false,"untrusted_external_content":true}`)
	if _, err := policy.Validate(ctx, job, cap, forged); err == nil {
		t.Fatal("unobserved provider item accepted")
	}
}

func TestServiceDeepSeekRouteRejectsPublicConfigAndMismatchedBody(t *testing.T) {
	_, p, store, job, cap := deepSeekWebJob(t)
	ctx := context.Background()
	policy := servicePluginPolicy{p: p, observed: map[string]bool{}, deepseek: map[string]deepSeekSearchEvidence{}, post: func(context.Context, string, map[string]string, json.RawMessage, int64, time.Duration) (json.RawMessage, error) {
		t.Fatal("provider called for rejected request")
		return nil, nil
	}}
	badBody := json.RawMessage(`{"route":"deepseek-search","body":{"query":"other","count":9,"need_content":false},"max_bytes":131072,"timeout_seconds":18}`)
	if _, err := policy.Service(ctx, job, cap, "wechat/http_route", badBody); err == nil {
		t.Fatal("mismatched query reached provider")
	}
	installed, err := store.Installed(ctx, job.Plugin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetEnabled(ctx, installed.Package.ID, installed.Revision, false); err != nil {
		t.Fatal(err)
	}
	config, err := store.Configuration(ctx, job.Plugin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveConfiguration(ctx, job.Plugin, pluginhost.ConfigUpdate{Revision: config.Revision, Values: map[string]json.RawMessage{"search_backend": json.RawMessage(`"public"`)}}); err != nil {
		t.Fatal(err)
	}
	installed, err = store.Installed(ctx, job.Plugin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetEnabled(ctx, installed.Package.ID, installed.Revision, true); err != nil {
		t.Fatal(err)
	}
	if _, err := policy.Service(ctx, job, cap, "wechat/http_route", json.RawMessage(`{"route":"deepseek-search","body":{"query":"synthetic search","count":9,"need_content":false},"max_bytes":131072,"timeout_seconds":18}`)); err == nil {
		t.Fatal("public backend accepted deepseek provider route")
	}
}

func TestServiceDeepSeekRouteForwardsLongUnicodeQuery(t *testing.T) {
	_, p, _, job, cap := deepSeekWebJob(t)
	ctx := context.Background()
	query := strings.Repeat("检索", 256) // 512 runes: deliberately beyond the old client-only preference.
	job.Arguments, _ = json.Marshal(map[string]any{"query": query, "count": 3})
	policy := servicePluginPolicy{p: p, observed: map[string]bool{}, deepseek: map[string]deepSeekSearchEvidence{}, post: func(_ context.Context, endpoint string, headers map[string]string, body json.RawMessage, maxBytes int64, timeout time.Duration) (json.RawMessage, error) {
		var bound struct {
			Query       string `json:"query"`
			Count       int    `json:"count"`
			NeedContent bool   `json:"need_content"`
		}
		if endpoint != "https://api.deepseek.com/v1/tools/search" || headers["Authorization"] != "Bearer synthetic-search-key" || maxBytes != 128<<10 || timeout != 18*time.Second || json.Unmarshal(body, &bound) != nil || bound.Query != query || bound.Count != 3 || bound.NeedContent {
			t.Fatalf("long query changed in route binding: endpoint=%q body=%s max=%d timeout=%s", endpoint, body, maxBytes, timeout)
		}
		return json.RawMessage(`{"results":[]}`), nil
	}}
	route, _ := json.Marshal(map[string]any{"route": "deepseek-search", "body": map[string]any{"query": query, "count": 3, "need_content": false}, "max_bytes": 128 << 10, "timeout_seconds": 18})
	if _, err := policy.Service(ctx, job, cap, "wechat/http_route", route); err != nil {
		t.Fatal(err)
	}
}

func TestServicePluginDiscoveryScopeAndUnverifiedResult(t *testing.T) {
	f, p := trustedAgent(t)
	ctx := context.Background()
	if len(p.servicePluginTools()) != 0 {
		t.Fatal("uninstalled plugin bloats tool list")
	}
	s, e := f.manager.packageStore()
	if e != nil {
		t.Fatal(e)
	}
	prepared, e := s.Prepare(ctx, "../../plugins/web-search")
	if e != nil {
		t.Fatal(e)
	}
	i, e := s.Install(ctx, prepared.Token, prepared.Digest, []string{"network"})
	if e != nil {
		t.Fatal(e)
	}
	executor := &packageTestExecutor{}
	h, e := pluginruntime.NewController(s, executor)
	if e != nil {
		t.Fatal(e)
	}
	f.manager.packageRuntime = h
	f.manager.packageRuntimeReady = true
	if len(p.servicePluginTools()) != 0 {
		t.Fatal("installed but disabled tools exposed")
	}
	if e = h.Start(ctx, i.Package.ID, i.Revision); e != nil {
		t.Fatal(e)
	}
	if len(p.servicePluginTools()) != 3 {
		t.Fatal("enabled service tools missing")
	}
	listed, e := p.List(ctx)
	if e != nil {
		t.Fatal(e)
	}
	counts := map[string]int{}
	for _, tool := range listed {
		counts[tool.Name]++
		if tool.Name == "web_fetch" {
			var schema map[string]any
			if json.Unmarshal(tool.InputSchema, &schema) != nil || schema["properties"].(map[string]any)["limit"] == nil {
				t.Fatal("old schema hid plugin pagination")
			}
		}
	}
	if counts["web_fetch"] != 1 || counts["web_search"] != 1 || p.ToolTimeout("web_fetch") != 0 {
		t.Fatal("duplicate tools or hidden timeout", counts)
	}
	// Existing tool names select the installed implementation; no provider
	// fallback or quota-bearing model call occurs after migration.
	if _, e = p.Call(ctx, "web_fetch", json.RawMessage(`{"url":"http://127.0.0.1:1/synthetic","limit":25}`)); e == nil || executor.calls != 1 {
		t.Fatal("legacy alias did not reach installed plugin", e, executor.calls)
	}
	raw, e := p.callServicePlugin(ctx, "service_plugins", json.RawMessage(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	var list struct {
		Plugins []any `json:"plugins"`
	}
	if json.Unmarshal([]byte(raw), &list) != nil || len(list.Plugins) != 1 {
		t.Fatal(raw)
	}
	// A fake transport cannot claim a completed webpage without the authorized
	// HTTP proxy having observed the source in this invocation.
	raw, e = p.callServicePlugin(ctx, "service_plugin_call", json.RawMessage(`{"plugin":"web-search","capability":"web.read","request_key":"read-original","arguments":{"url":"https://example.test/page"}}`))
	if e == nil {
		t.Fatal("unobserved result accepted", raw)
	}
	var outcome struct {
		ID    string `json:"task_id"`
		State string `json:"state"`
	}
	if json.Unmarshal([]byte(raw), &outcome) != nil || outcome.ID == "" || outcome.State != "needs_review" {
		t.Fatal("unknown result lost original task", raw, e)
	}
	args, _ := json.Marshal(map[string]string{"task_id": outcome.ID})
	if _, e = p.callServicePlugin(ctx, "service_plugin_task", args); e != nil {
		t.Fatal(e)
	}
	p.trace.Sender = "another-user"
	if _, e = p.callServicePlugin(ctx, "service_plugin_task", args); e == nil {
		t.Fatal("other requester read task")
	}
	i, e = s.Installed(ctx, i.Package.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = h.Stop(ctx, i.Package.ID, i.Revision); e != nil {
		t.Fatal(e)
	}
	p.trace.Sender = "wxid_owner"
	if p.enabled("web_search") || p.enabled("web_fetch") {
		t.Fatal("disabled plugin fell back to old built-ins")
	}
}

func TestServiceResultForgetAndLiveRevocationApplyToRetainedPages(t *testing.T) {
	f, p := trustedAgent(t)
	ctx := context.Background()
	s, e := f.manager.packageStore()
	if e != nil {
		t.Fatal(e)
	}
	prepared, e := s.Prepare(ctx, "../../plugins/web-search")
	if e != nil {
		t.Fatal(e)
	}
	i, e := s.Install(ctx, prepared.Token, prepared.Digest, []string{"network"})
	if e != nil {
		t.Fatal(e)
	}
	i, e = s.SetEnabled(ctx, i.Package.ID, i.Revision, true)
	if e != nil {
		t.Fatal(e)
	}
	synthetic := p.trace
	synthetic.Text = "Synthetic public webpage request"
	if e = f.manager.memory.Observe(synthetic, f.manager.Config().Companion, time.Now()); e != nil {
		t.Fatal(e)
	}
	epoch, e := f.manager.memory.RecallEpoch(ctx)
	if e != nil {
		t.Fatal(e)
	}
	j := pluginhost.Job{Plugin: i.Package.ID, Version: i.Package.Version, PackageDigest: i.Digest, ConfigRevision: "0", Tool: "web.read", Arguments: json.RawMessage(`{"url":"https://example.test/page"}`), Context: pluginhost.Invocation{Account: p.trace.Account, Thread: p.trace.Thread, Requester: p.trace.Sender, Purpose: "synthetic", RequestKey: "one", EvidenceEpoch: epoch, Authorization: p.serviceAuthorization()}}
	j, _, e = s.CreateJob(ctx, j)
	if e != nil {
		t.Fatal(e)
	}
	j, e = s.ClaimJob(ctx, j.ID, j.Revision)
	if e != nil {
		t.Fatal(e)
	}
	raw := json.RawMessage(`{"ok":true,"url":"https://example.test/page","text":"synthetic","untrusted_external_content":true}`)
	policy := servicePluginPolicy{p: p, observed: map[string]bool{"https://example.test/page": true}}
	if _, e = policy.Validate(ctx, j, pluginhost.Capability{}, raw); e != nil {
		t.Fatal(e)
	}
	if _, e = (servicePluginPolicy{p: p}).Validate(ctx, j, pluginhost.Capability{}, raw); e == nil {
		t.Fatal("forged source passed")
	}
	j, e = s.CompleteJob(ctx, j.ID, j.Revision, nil, raw)
	if e != nil {
		t.Fatal(e)
	}
	envelope, _ := json.Marshal(pluginruntime.CallResult{Job: j})
	id, e := p.RetainToolOutput(ctx, "service_plugin_call", string(envelope))
	if e != nil || id == "" {
		t.Fatal(id, e)
	}
	pageArgs, _ := json.Marshal(map[string]string{"result_id": id})
	if _, e = p.readToolOutput(ctx, "tool_output_read", pageArgs); e != nil {
		t.Fatal(e)
	}
	f.manager.mu.Lock()
	f.manager.mcp.WebFetchEnabled = false
	f.manager.mu.Unlock()
	if _, e = p.readToolOutput(ctx, "tool_output_read", pageArgs); e == nil {
		t.Fatal("live revocation did not apply")
	}
	f.manager.mu.Lock()
	f.manager.mcp.WebFetchEnabled = true
	f.manager.mu.Unlock()
	if e = f.manager.memory.Clear(companion.ScopeID("wechat", p.trace.Account, p.trace.Thread)); e != nil {
		t.Fatal(e)
	}
	if _, e = p.readToolOutput(ctx, "tool_output_read", pageArgs); e == nil {
		t.Fatal("forgotten context exposed in retained plugin page")
	}
}

func TestPublicPluginKeepsMemberAndAuthorizedSurfAccessWithoutMemory(t *testing.T) {
	f, p := trustedAgent(t)
	f.manager.mu.Lock()
	f.manager.config.DirectIDs = append(f.manager.config.DirectIDs, "wxid_member")
	f.manager.config.Companion.Permissions.Remember = false
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.Proactive = true
	f.manager.config.Companion.QuietStart = "00:00"
	f.manager.config.Companion.QuietEnd = "00:00"
	f.manager.surf.Settings.Enabled = true
	f.manager.surf.Settings.Threads = []string{"wxid_member"}
	f.manager.mu.Unlock()
	p.trace.Thread = "wxid_member"
	p.trace.Sender = "wxid_member"
	if p.privileged() || !p.servicePluginsAllowed() || p.serviceAuthorization() == "" {
		t.Fatal("ordinary authorized public reading requires owner or memory")
	}
	p.trace.Surf = true
	p.trace.Sender = ""
	if !p.servicePluginsAllowed() || p.serviceRequester() != "host:surf" || p.serviceAuthorization() == "" {
		t.Fatal("authorized autonomous public reading lost")
	}
	f.manager.mu.Lock()
	f.manager.surf.Settings.Enabled = false
	f.manager.mu.Unlock()
	if p.servicePluginsAllowed() {
		t.Fatal("revoked surf still authorized")
	}
}
