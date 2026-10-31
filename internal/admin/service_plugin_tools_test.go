package admin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

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
