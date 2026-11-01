package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"wechat-bot/internal/companion"

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/plugins"
)

type externalMCPSyntheticExecutor struct{}

func (e *externalMCPSyntheticExecutor) Start(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
	return e, nil
}
func (*externalMCPSyntheticExecutor) Health(context.Context) error { return nil }
func (*externalMCPSyntheticExecutor) Close() error                 { return nil }
func (*externalMCPSyntheticExecutor) Call(ctx context.Context, _ pluginhost.Invocation, _ string, raw json.RawMessage) (json.RawMessage, error) {
	var args externalMCPArguments
	if json.Unmarshal(raw, &args) != nil {
		return nil, errors.New("request")
	}
	request, _ := json.Marshal(map[string]any{"operation": args.Operation, "request_sha256": args.RequestSHA256})
	observed, err := pluginruntime.InvokeHostService(ctx, "wechat/external_mcp", request)
	if err != nil {
		return nil, err
	}
	var response struct {
		RawResult json.RawMessage     `json:"raw_result"`
		BodyRef   externalHTTPBlobRef `json:"body_ref"`
	}
	if json.Unmarshal(observed, &response) != nil {
		return nil, errors.New("response")
	}
	result := externalMCPWireResult{Observation: response.BodyRef}
	var protocol struct {
		Allowlist []string `json:"allowlist"`
	}
	_ = json.Unmarshal(args.Protocol, &protocol)
	if args.Operation == "list" {
		var page struct {
			Tools []struct {
				Name, Description string
				InputSchema       json.RawMessage
			}
			NextCursor string
		}
		_ = json.Unmarshal(response.RawResult, &page)
		result.Tools = []mcptools.Tool{}
		for _, tool := range page.Tools {
			if len(protocol.Allowlist) > 0 && !containsMCPName(protocol.Allowlist, tool.Name) {
				continue
			}
			schema := tool.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			result.Tools = append(result.Tools, mcptools.Tool{Name: args.ServerID + "__" + tool.Name, Description: tool.Description, InputSchema: schema})
		}
		result.NextCursor = page.NextCursor
	} else {
		var object map[string]json.RawMessage
		_ = json.Unmarshal(response.RawResult, &object)
		var content []map[string]any
		_ = json.Unmarshal(object["content"], &content)
		output := struct {
			Text  string             `json:"text"`
			Media []externalMCPMedia `json:"media,omitempty"`
		}{Media: []externalMCPMedia{}}
		texts := []string{}
		for i, item := range content {
			kind, _ := item["type"].(string)
			if kind == "text" {
				text, _ := item["text"].(string)
				texts = append(texts, text)
			} else if kind == "image" || kind == "audio" {
				mediaKind := "image"
				if kind == "audio" {
					mediaKind = "voice"
				}
				ref, _ := item["host_media_ref"].(string)
				mime, _ := item["mimeType"].(string)
				output.Media = append(output.Media, externalMCPMedia{Kind: mediaKind, ContentIndex: i, HostMediaRef: ref, MIMEType: mime})
			}
		}
		output.Text = strings.Join(texts, "\n")
		encoded, _ := json.Marshal(output)
		wrapper, _ := json.Marshal(map[string]any{"observation": response.BodyRef, "output": json.RawMessage(encoded), "is_error": json.RawMessage(object["isError"])})
		var values map[string]json.RawMessage
		_ = json.Unmarshal(wrapper, &values)
		if len(object["isError"]) == 0 {
			delete(values, "is_error")
		}
		return json.Marshal(values)
	}
	return json.Marshal(result)
}
func containsMCPName(values []string, name string) bool {
	for _, value := range values {
		if value == name {
			return true
		}
	}
	return false
}
func externalMCPFixture(t *testing.T, endpoint string, installed bool) (*webFixture, *permittedTools) {
	t.Helper()
	f, p := trustedAgent(t)
	settings := mcpDefaults()
	settings.Servers = []mcptools.ServerConfig{{ID: "synthetic", Enabled: true, Transport: "http", URL: endpoint, Tools: []string{"echo", "second"}, Headers: map[string]string{"Authorization": "Bearer synthetic-mcp-secret"}}}
	f.manager.mu.Lock()
	f.manager.mcp = cloneMCP(settings)
	f.manager.mu.Unlock()
	p.settings = cloneMCP(settings)
	p.mcpSourceHash = externalMCPSourceHash(settings, f.manager.plugins.installed())
	var err error
	p.runtime, err = plugins.New(f.manager.plugins.installed())
	if err != nil {
		t.Fatal(err)
	}
	p.hub, err = f.manager.newMCPHub(settings.Servers)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.hub.Close() })
	if installed {
		installExternalMCPTestRuntime(t, p)
	}
	return f, p
}
func installExternalMCPTestRuntime(t *testing.T, p *permittedTools) *pluginhost.Store {
	t.Helper()
	store := installExternalHTTPFixture(t, p)
	controller, err := pluginruntime.NewController(store, &externalMCPSyntheticExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	p.manager.packageRuntime, p.manager.packageRuntimeReady = controller, true
	t.Cleanup(func() { _ = controller.Close() })
	return store
}
func externalMCPProvider(t *testing.T, calls *atomic.Int32, text string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(200)
			return
		}
		var request struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("rpc decode")
			return
		}
		if request.Method == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05"}
		case "tools/list":
			if string(request.Params["cursor"]) == `"page2"` {
				result = map[string]any{"tools": []map[string]any{{"name": "second", "description": "Second", "inputSchema": map[string]any{"type": "object"}}}}
			} else {
				result = map[string]any{"tools": []map[string]any{{"name": "echo", "description": "Echo", "inputSchema": map[string]any{"type": "object"}}, {"name": "hidden", "inputSchema": map[string]any{"type": "object"}}}, "nextCursor": "page2"}
			}
		case "tools/call":
			calls.Add(1)
			if r.Header.Get("Authorization") != "Bearer synthetic-mcp-secret" {
				t.Error("credential absent")
			}
			if !strings.Contains(string(request.Params["arguments"]), "18446744073709551615") {
				t.Error("argument numeric precision changed", string(request.Params["arguments"]))
			}
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}
		default:
			t.Error("unexpected RPC", request.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
}
func TestExternalMCPInstalledListPagesAndCallCheckpoint(t *testing.T) {
	var calls atomic.Int32
	text := strings.Repeat("synthetic text ", 3000)
	provider := externalMCPProvider(t, &calls, text)
	defer provider.Close()
	_, p := externalMCPFixture(t, provider.URL, true)
	tools, err := p.installedExternalMCPTools(t.Context())
	if err != nil || len(tools) != 2 || tools[0].Name != "synthetic__echo" || tools[1].Name != "synthetic__second" {
		t.Fatal(tools, err)
	}
	args := json.RawMessage(`{"integer":18446744073709551615}`)
	for range 2 {
		result, err := p.Call(t.Context(), "synthetic__echo", args)
		if err != nil || result != text {
			t.Fatal("full text failed", len(result), err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("replayed", calls.Load())
	}
}
func TestExternalMCPMissingPackageAndConfigurationChangeDoNotCall(t *testing.T) {
	var calls atomic.Int32
	provider := externalMCPProvider(t, &calls, "text")
	defer provider.Close()
	f, p := externalMCPFixture(t, provider.URL, false)
	if _, err := p.Call(t.Context(), "synthetic__echo", json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing plugin fallback")
	}
	if calls.Load() != 0 {
		t.Fatal(calls.Load())
	}
	installExternalHTTPFixture(t, p)
	f.manager.mu.Lock()
	f.manager.mcp.Servers[0].Headers["Authorization"] = "changed"
	f.manager.mu.Unlock()
	if _, err := p.newExternalMCPPolicy(t.Context(), "call", p.externalMCPServers()[0], "echo", "", json.RawMessage(`{}`)); err == nil {
		t.Fatal("stale hub admitted")
	}
}
func TestExternalMCPManualRoutesUseSessionIdentityAndNoSender(t *testing.T) {
	var calls atomic.Int32
	provider := externalMCPProvider(t, &calls, "manual MCP result")
	defer provider.Close()
	f, _ := externalMCPFixture(t, provider.URL, true)
	native := &fakeTools{fakeNative: f.native}
	f.manager.native = native
	cfg := f.manager.agent.config()
	cfg.ExternalOwnerOnly = true
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"name": "synthetic__echo", "arguments": json.RawMessage(`{"integer":18446744073709551615}`)}
	if code, _, _ := f.request("POST", "/api/mcp/test", input, true, false); code != 401 {
		t.Fatal(code)
	}
	f.login()
	if code, _, _ := f.request("POST", "/api/mcp/test", input, true, false); code != 403 {
		t.Fatal(code)
	}
	if code, body, _ := f.request("GET", "/api/mcp/tools", nil, true, true); code != 200 || !strings.Contains(string(body), "synthetic__echo") {
		t.Fatal(code, string(body))
	}
	if code, body, _ := f.request("POST", "/api/mcp/test", input, true, true); code != 200 || !strings.Contains(string(body), "manual MCP result") {
		t.Fatal(code, string(body))
	}
	if calls.Load() != 1 || native.submits.Load() != 0 {
		t.Fatal(calls.Load(), native.submits.Load())
	}
	store, _ := f.manager.packageStore()
	page, err := store.Jobs(t.Context(), externalHTTPPluginID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range page.Items {
		if !strings.HasPrefix(job.Context.Thread, "host:external-mcp-") || job.Context.Requester != "administrator" || len(job.Context.SourceVersions) != 0 || strings.Contains(string(job.Arguments), "synthetic-mcp-secret") || strings.Contains(string(job.Arguments), provider.URL) {
			t.Fatal("manual MCP invented chat authority or leaked credentials", job.Context)
		}
	}
}
func TestExternalMCPUnknownCallIsNotResubmitted(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(200)
			return
		}
		var q struct {
			ID     json.RawMessage
			Method string
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		if q.Method == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		if q.Method == "tools/call" {
			calls.Add(1)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": map[string]any{"protocolVersion": "2024-11-05"}})
	}))
	defer provider.Close()
	_, p := externalMCPFixture(t, provider.URL, true)
	for range 2 {
		if _, err := p.Call(t.Context(), "synthetic__echo", json.RawMessage(`{}`)); err == nil {
			t.Fatal("unknown call succeeded")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("unknown call replayed", calls.Load())
	}
}
func TestExternalMCPBrokerRejectsForgeryAndForgottenSource(t *testing.T) {
	var calls atomic.Int32
	provider := externalMCPProvider(t, &calls, "actual MCP fact")
	defer provider.Close()
	f, p := externalMCPFixture(t, provider.URL, true)
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.mu.Unlock()
	p.trace.Text = "synthetic MCP source"
	if err := f.manager.memory.Observe(p.trace, f.manager.Config().Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	p.trace.Source, _ = f.manager.memory.SourceForInput(p.trace)
	policy, err := p.newExternalMCPPolicy(t.Context(), "call", p.externalMCPServers()[0], "echo", "", json.RawMessage(`{"integer":18446744073709551615}`))
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := policy.store.CreateJob(t.Context(), pluginhost.Job{Plugin: externalHTTPPluginID, Tool: externalMCPCapability, Version: policy.installed.Package.Version, PackageDigest: policy.installed.Digest, ConfigRevision: "1", Context: policy.invocation, Arguments: policy.arguments})
	if err != nil {
		t.Fatal(err)
	}
	job, err = policy.store.ClaimJob(t.Context(), job.ID, job.Revision)
	if err != nil {
		t.Fatal(err)
	}
	cap := pluginhost.Capability{ID: externalMCPCapability, Permissions: []string{"network"}, Implementation: "ready", Discoverable: true}
	bad, _ := json.Marshal(map[string]string{"operation": "call", "request_sha256": "invented"})
	if _, err = policy.Service(t.Context(), job, cap, "wechat/external_mcp", bad); err == nil {
		t.Fatal("changed hash accepted")
	}
	request, _ := json.Marshal(map[string]string{"operation": "call", "request_sha256": policy.mcp.request.RequestSHA256})
	if _, err = policy.Service(t.Context(), job, cap, "wechat/external_mcp", request); err != nil {
		t.Fatal(err)
	}
	if _, err = policy.Service(t.Context(), job, cap, "wechat/external_mcp", request); err == nil {
		t.Fatal("callback replay accepted")
	}
	forged, _ := json.Marshal(map[string]any{"observation": policy.body, "output": map[string]any{"text": "invented", "media": []any{}}})
	if _, err = policy.Validate(t.Context(), job, cap, forged); err == nil {
		t.Fatal("invented output accepted")
	}
	if err = f.manager.memory.Clear(companion.ScopeID("wechat", p.trace.Account, p.trace.Thread)); err != nil {
		t.Fatal(err)
	}
	if err = policy.Check(t.Context(), job, cap); err == nil {
		t.Fatal("forgotten source survived")
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}
func TestLiveExternalMCPInstalledLPACUsesHostTransport(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in installed MCP LPAC with local synthetic server")
	}
	var calls atomic.Int32
	text := strings.Repeat("isolated MCP ", 3000)
	provider := externalMCPProvider(t, &calls, text)
	defer provider.Close()
	f, p := externalMCPFixture(t, provider.URL, false)
	store := installExternalHTTPFixture(t, p)
	python, _ = filepath.Abs(python)
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	controller, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	t.Cleanup(func() { _ = controller.Close() })
	tools, err := p.installedExternalMCPTools(t.Context())
	if err != nil || len(tools) != 2 {
		t.Fatal(tools, err)
	}
	for range 2 {
		result, err := p.Call(t.Context(), "synthetic__echo", json.RawMessage(`{"integer":18446744073709551615}`))
		if err != nil || result != text {
			t.Fatal(len(result), err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}
func TestExternalMCPMediaReferencesRetainEveryActualBlock(t *testing.T) {
	data := []byte(`{"content":[{"type":"text","text":"actual"},{"type":"image","data":"AQ==","mimeType":"image/png"},{"type":"audio","data":"Ag==","mimeType":"audio/wav"},{"type":"image","data":"Aw==","mimeType":"image/png"},{"type":"image","data":"BA==","mimeType":"image/png"}]}`)
	ref := externalHTTPBlobRef{SHA256: namedSpeechSHA(data), Size: int64(len(data))}
	observation, err := externalMCPObservation(data, ref)
	if err != nil || strings.Contains(string(observation), `"data"`) {
		t.Fatal(string(observation), err)
	}
	policy := &externalHTTPPolicy{mcp: &externalMCPAuthority{request: externalMCPArguments{Operation: "call"}}}
	var result externalMCPWireResult
	raw, _ := json.Marshal(map[string]any{"observation": ref, "output": map[string]any{"text": "actual", "media": []externalMCPMedia{{Kind: "image", ContentIndex: 1, HostMediaRef: namedSpeechSHA([]byte{1}), MIMEType: "image/png"}, {Kind: "voice", ContentIndex: 2, HostMediaRef: namedSpeechSHA([]byte{2}), MIMEType: "audio/wav"}, {Kind: "image", ContentIndex: 3, HostMediaRef: namedSpeechSHA([]byte{3}), MIMEType: "image/png"}, {Kind: "image", ContentIndex: 4, HostMediaRef: namedSpeechSHA([]byte{4}), MIMEType: "image/png"}}}})
	_ = json.Unmarshal(raw, &result)
	output, err := policy.mcpResultProof(result, data, ref)
	if err != nil || len(output.Media) != 4 || output.Media[3].Data[0] != 4 {
		t.Fatal(output, err)
	}
	result.Output.Media[3].ContentIndex = 1
	if _, err = policy.mcpResultProof(result, data, ref); err == nil {
		t.Fatal("media index forgery accepted")
	}
}
func TestExternalMCPSourceHashIncludesOnlyCompleteMCPDeclarations(t *testing.T) {
	settings := mcpDefaults()
	mcp := plugins.Installed{Enabled: true, Manifest: plugins.Manifest{ID: "synthetic", MCP: &mcptools.ServerConfig{Transport: "http", URL: "https://synthetic.invalid/mcp"}, ToolMediaKinds: map[string]string{"echo": "voice"}}, Secrets: map[string]string{"UNUSED": "original"}}
	httpOnly := plugins.Installed{Manifest: plugins.Manifest{ID: "http_only"}, Secrets: map[string]string{"TOKEN": "original"}}
	items := []plugins.Installed{mcp, httpOnly}
	original := externalMCPSourceHash(settings, items)
	httpOnly.Secrets["TOKEN"] = "changed"
	if externalMCPSourceHash(settings, items) != original {
		t.Fatal("unrelated HTTP credentials revoked MCP")
	}
	mcp.Secrets["UNUSED"] = "changed"
	if externalMCPSourceHash(settings, items) == original {
		t.Fatal("same MCP unused credential was not frozen")
	}
	mcp.Secrets["UNUSED"] = "original"
	mcp.Manifest.ToolMediaKinds["echo"] = "file"
	if externalMCPSourceHash(settings, items) == original {
		t.Fatal("MCP media authorization classification was not frozen")
	}
}
