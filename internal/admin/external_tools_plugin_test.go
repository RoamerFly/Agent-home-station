package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/plugins"
)

const externalTestName = "synthetic_api__echo"

func externalLegacyFixture(t *testing.T, endpoint string) (*webFixture, *permittedTools) {
	t.Helper()
	f, p := trustedAgent(t)
	entry := pluginEntry{Installed: plugins.Installed{Enabled: true, Manifest: plugins.Manifest{ID: "synthetic_api", Name: "Synthetic API", Version: "1", Tools: []plugins.APITool{{Name: "echo", Description: "Synthetic one-step HTTP tool", InputSchema: json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`), URL: endpoint, Method: "POST", Headers: map[string]string{"Authorization": "Bearer {{secret.TOKEN}}"}, BodyTemplate: json.RawMessage(`{"input":"{{arg.input}}"}`), TextPath: "result.text"}}}, Secrets: map[string]string{"TOKEN": "synthetic-host-secret"}}}
	if err := f.manager.plugins.save(entry); err != nil {
		t.Fatal(err)
	}
	runtime, err := plugins.New(f.manager.plugins.installed())
	if err != nil {
		t.Fatal(err)
	}
	p.runtime = runtime
	p.apiNames = map[string]bool{externalTestName: true}
	p.apiKinds = map[string]string{}
	return f, p
}
func installExternalHTTPFixture(t *testing.T, p *permittedTools) *pluginhost.Store {
	t.Helper()
	s, err := p.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := s.Prepare(t.Context(), "../../plugins/external-tools")
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveConfiguration(t.Context(), externalHTTPPluginID, pluginhost.ConfigUpdate{Values: map[string]json.RawMessage{}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetEnabled(t.Context(), externalHTTPPluginID, i.Revision, true); err != nil {
		t.Fatal(err)
	}
	return s
}

type externalSyntheticExecutor struct{}

func (e *externalSyntheticExecutor) Start(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
	return e, nil
}
func (e *externalSyntheticExecutor) Health(context.Context) error { return nil }
func (e *externalSyntheticExecutor) Close() error                 { return nil }
func (e *externalSyntheticExecutor) Call(ctx context.Context, _ pluginhost.Invocation, name string, raw json.RawMessage) (json.RawMessage, error) {
	var args externalHTTPArguments
	if json.Unmarshal(raw, &args) != nil {
		return nil, errors.New("synthetic invalid request")
	}
	requestValue := map[string]any{"phase": args.Phase, "tool": args.Tool, "arguments_sha256": args.ArgumentsSHA256}
	if args.Ticket != nil {
		requestValue["ticket_sha256"] = args.TicketSHA256
	}
	request, _ := json.Marshal(requestValue)
	observed, err := pluginruntime.InvokeHostService(ctx, "wechat/external_request", request)
	if err != nil {
		return nil, err
	}
	var reply externalHTTPObservation
	_ = json.Unmarshal(observed, &reply)
	if name == externalPollCapability {
		return syntheticExternalPollReply(args, reply)
	}
	if args.Phase != "call" {
		return syntheticExternalJobReply(args, reply)
	}
	output := externalHTTPWireOutput{}
	if reply.BinaryRef != nil {
		kind := ""
		var protocol plugins.ExternalProtocol
		_ = json.Unmarshal(args.Protocol, &protocol)
		for _, tool := range protocol.Tools {
			if tool.Name == args.Tool {
				kind = tool.MediaKind
			}
		}
		output.Media = []externalHTTPWireMedia{{MediaResult: plugins.MediaResult{Kind: kind}, HostBinaryRef: reply.BinaryRef.SHA256}}
	} else {
		data, _ := base64.StdEncoding.DecodeString(reply.BodyBase64)
		var object struct{ Result struct{ Text string } }
		if json.Unmarshal(data, &object) != nil {
			return nil, errors.New("synthetic non-json")
		}
		output.Text = object.Result.Text
	}
	return json.Marshal(externalHTTPWireResult{Observation: reply, Output: output})
}
func externalHTTPFixture(t *testing.T, endpoint string) (*webFixture, *permittedTools, *pluginhost.Store) {
	t.Helper()
	f, p := externalLegacyFixture(t, endpoint)
	s := installExternalHTTPTestRuntime(t, p)
	return f, p, s
}
func installExternalHTTPTestRuntime(t *testing.T, p *permittedTools) *pluginhost.Store {
	t.Helper()
	s := installExternalHTTPFixture(t, p)
	c, err := pluginruntime.NewController(s, &externalSyntheticExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	p.manager.packageRuntime, p.manager.packageRuntimeReady = c, true
	t.Cleanup(func() { _ = c.Close() })
	return s
}
func externalHTTPJob(t *testing.T, p *permittedTools) (*externalHTTPPolicy, pluginhost.Job, pluginhost.Capability) {
	t.Helper()
	policy, err := p.newExternalHTTPPolicy(t.Context(), externalTestName, json.RawMessage(`{"input":"synthetic"}`))
	if err != nil {
		t.Fatal(err)
	}
	j, _, err := policy.store.CreateJob(t.Context(), pluginhost.Job{Plugin: externalHTTPPluginID, Tool: externalHTTPCapability, Version: policy.installed.Package.Version, PackageDigest: policy.installed.Digest, ConfigRevision: "1", Context: policy.invocation, Arguments: policy.arguments})
	if err != nil {
		t.Fatal(err)
	}
	j, err = policy.store.ClaimJob(t.Context(), j.ID, j.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return policy, j, pluginhost.Capability{ID: externalHTTPCapability, Permissions: []string{"network"}, Implementation: "ready", Discoverable: true}
}
func externalServiceArgs(policy *externalHTTPPolicy) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"phase": "call", "tool": policy.request.Tool, "arguments_sha256": policy.request.ArgumentsSHA256})
	return raw
}

func TestExternalHTTPMissingPackageHasNoSynchronousFallback(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"text":"forbidden fallback"}}`))
	}))
	defer server.Close()
	_, p := externalLegacyFixture(t, server.URL)
	for _, tool := range p.localTools() {
		if tool.Name == externalTestName {
			t.Fatal("uninstalled package exposed synchronous HTTP tool")
		}
	}
	if _, err := p.Call(t.Context(), externalTestName, json.RawMessage(`{"input":"synthetic"}`)); err == nil {
		t.Fatal("uninstalled API fell back to core Runtime.Call")
	}
	if calls.Load() != 0 {
		t.Fatal("uninstalled package sent an HTTP request")
	}
}

func TestExternalHTTPControllerUsesOneBoundHostRequestAndPrivateCheckpoint(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic-host-secret" {
			t.Error("host credential was not applied")
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["input"] != "synthetic" {
			t.Error(body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"text":"actual synthetic response"}}`))
	}))
	defer server.Close()
	_, p, store := externalHTTPFixture(t, server.URL)
	args := json.RawMessage(`{"input":"synthetic"}`)
	for range 2 {
		result, err := p.Call(t.Context(), externalTestName, args)
		if err != nil || !strings.Contains(result, "actual synthetic response") {
			t.Fatal(result, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("same invocation replayed HTTP", calls.Load())
	}
	page, err := store.Jobs(t.Context(), externalHTTPPluginID, 0, 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	job := page.Items[0]
	if strings.Contains(string(job.Arguments), server.URL) || strings.Contains(string(job.Arguments), "synthetic-host-secret") || strings.Contains(string(job.Arguments), "Authorization") || strings.Contains(string(job.Result), "actual synthetic response") {
		t.Fatal("endpoint/credential entered package arguments or response escaped private checkpoint")
	}
}

func TestExternalHTTPBrokerRejectsChangedRequestResultAndLegacySecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"text":"actual"}}`))
	}))
	defer server.Close()
	f, p, _ := externalHTTPFixture(t, server.URL)
	policy, j, cap := externalHTTPJob(t, p)
	if _, err := policy.Service(t.Context(), j, cap, "wechat/external_request", json.RawMessage(`{"phase":"call","tool":"other","arguments":{}}`)); err == nil {
		t.Fatal("plugin changed the bound HTTP tool")
	}
	observed, err := policy.Service(t.Context(), j, cap, "wechat/external_request", externalServiceArgs(policy))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = policy.Service(t.Context(), j, cap, "wechat/external_request", externalServiceArgs(policy)); err == nil {
		t.Fatal("plugin replayed HTTP callback")
	}
	var reply plugins.ExternalResponse
	_ = json.Unmarshal(observed, &reply)
	forged, _ := json.Marshal(externalHTTPResult{Observation: reply, Output: plugins.Output{Text: "invented"}})
	if _, err = policy.Validate(t.Context(), j, cap, forged); err == nil {
		t.Fatal("plugin invented provider output")
	}
	entry, _ := f.manager.plugins.get("synthetic_api")
	entry.Secrets["TOKEN"] = "changed-host-secret"
	if err = f.manager.plugins.save(entry); err != nil {
		t.Fatal(err)
	}
	if err = policy.Check(t.Context(), j, cap); err == nil {
		t.Fatal("old invocation survived a legacy credential change")
	}
}

func TestExternalHTTPUnknownTransportIsNotResubmitted(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		connection, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = connection.Close()
		}
	}))
	defer server.Close()
	_, p, _ := externalHTTPFixture(t, server.URL)
	for range 2 {
		if _, err := p.callExternalHTTP(t.Context(), externalTestName, json.RawMessage(`{"input":"synthetic"}`)); err == nil {
			t.Fatal("unknown HTTP submission became successful")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("unknown HTTP submission was replayed", calls.Load())
	}
}

func TestExternalHTTPForgottenSourceCannotIssueTheBoundRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	f, p, _ := externalHTTPFixture(t, server.URL)
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.mu.Unlock()
	p.trace.Text = "synthetic source request"
	if err := f.manager.memory.Observe(p.trace, f.manager.Config().Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	p.trace.Source, _ = f.manager.memory.SourceForInput(p.trace)
	policy, job, cap := externalHTTPJob(t, p)
	if err := f.manager.memory.Clear(companion.ScopeID("wechat", p.trace.Account, p.trace.Thread)); err != nil {
		t.Fatal(err)
	}
	if _, err := policy.Service(t.Context(), job, cap, "wechat/external_request", externalServiceArgs(policy)); err == nil || calls.Load() != 0 {
		t.Fatal("forgotten source authorized HTTP", err, calls.Load())
	}
}

func TestExternalHTTPCheckpointRejectsModifiedPrivateBytes(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"text":"original observed text"}}`))
	}))
	defer server.Close()
	f, p, store := externalHTTPFixture(t, server.URL)
	args := json.RawMessage(`{"input":"synthetic"}`)
	if _, err := p.callExternalHTTP(t.Context(), externalTestName, args); err != nil {
		t.Fatal(err)
	}
	page, err := store.Jobs(t.Context(), externalHTTPPluginID, 0, 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	path := filepath.Join(f.manager.privateWorkspaceFor(p.trace.Account), "external-http-replies", page.Items[0].ID, "reply.bin")
	if err := os.WriteFile(path, []byte("changed private bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.callExternalHTTP(t.Context(), externalTestName, args); err == nil || calls.Load() != 1 {
		t.Fatal("corrupt private checkpoint was trusted or HTTP was replayed", err, calls.Load())
	}
}

func TestExternalHTTPStoredCancellationStopsInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	_, p, _ := externalHTTPFixture(t, server.URL)
	policy, j, cap := externalHTTPJob(t, p)
	done := make(chan error, 1)
	go func() {
		_, err := policy.Service(t.Context(), j, cap, "wechat/external_request", externalServiceArgs(policy))
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP never started")
	}
	if _, err := policy.store.RequestJobCancel(t.Context(), j.ID, j.Revision); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled invocation returned a provider result")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("stored cancellation did not cancel HTTP")
	}
}

func TestExternalHTTPManualRouteUsesSessionProofWithoutWeChatActor(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"text":"manual synthetic result"}}`))
	}))
	defer server.Close()
	f, _, store := externalHTTPFixture(t, server.URL)
	native := &fakeTools{fakeNative: f.native}
	f.manager.native = native
	cfg := f.manager.agent.config()
	cfg.ExternalOwnerOnly = true
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"name": externalTestName, "arguments": map[string]string{"input": "synthetic"}}
	if code, _, _ := f.request("POST", "/api/mcp/test", input, true, false); code != 401 {
		t.Fatal("unauthenticated manual request", code)
	}
	f.login()
	if code, _, _ := f.request("POST", "/api/mcp/test", input, true, false); code != 403 {
		t.Fatal("manual request bypassed CSRF", code)
	}
	code, body, _ := f.request("POST", "/api/mcp/test", input, true, true)
	if code != 200 || !strings.Contains(string(body), "manual synthetic result") || calls.Load() != 1 || native.submits.Load() != 0 {
		t.Fatal("authenticated manual API test failed or published a message", code, string(body), calls.Load(), native.submits.Load())
	}
	page, err := store.Jobs(t.Context(), externalHTTPPluginID, 0, 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].Context.Thread != "host:external-test" || page.Items[0].Context.Requester != "administrator" || len(page.Items[0].Context.SourceVersions) != 0 {
		t.Fatal("management test fabricated a WeChat actor/source", page, err)
	}
}

func TestExternalHTTPManualRouteRevokedSessionCannotComplete(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		calls.Add(1)
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	f, _, store := externalHTTPFixture(t, server.URL)
	f.login()
	input := map[string]any{"name": externalTestName, "arguments": map[string]string{"input": "synthetic"}}
	installed, err := store.Installed(t.Context(), externalHTTPPluginID)
	if err != nil {
		t.Fatal(err)
	}
	installed, err = store.SetEnabled(t.Context(), externalHTTPPluginID, installed.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.request("POST", "/api/mcp/test", input, true, true); code != 502 || calls.Load() != 0 {
		t.Fatal("manual test bypassed disabled installed package", code, calls.Load())
	}
	if _, err := store.SetEnabled(t.Context(), externalHTTPPluginID, installed.Revision, true); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { code, _, _ := f.request("POST", "/api/mcp/test", input, true, true); done <- code }()
	select {
	case <-started:
	case <-time.After(4 * time.Second):
		t.Fatal("manual HTTP request never started")
	}
	if code, _, _ := f.request("POST", "/api/logout", nil, true, true); code != 200 {
		t.Fatal("logout failed", code)
	}
	select {
	case code := <-done:
		if code != 502 || calls.Load() != 1 {
			t.Fatal("revoked session completed or replayed HTTP", code, calls.Load())
		}
	case <-time.After(4 * time.Second):
		t.Fatal("session revocation did not cancel the manual HTTP request")
	}
}

func setExternalBinaryDescriptor(t *testing.T, f *webFixture, p *permittedTools, endpoint string) {
	t.Helper()
	entry, _ := f.manager.plugins.get("synthetic_api")
	entry.Manifest.Tools[0].TextPath = ""
	entry.Manifest.Tools[0].MediaKind = "file"
	entry.Manifest.Tools[0].URL = endpoint
	if err := f.manager.plugins.save(entry); err != nil {
		t.Fatal(err)
	}
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.SendFile = true
	f.manager.mu.Unlock()
	p.trace.TraceID = "synthetic-binary-request"
}
func TestExternalHTTPLargeBodiesUsePrivateFilesAndBinaryReferences(t *testing.T) {
	binary := bytes.Repeat([]byte{0x7f}, 25<<20)
	text := strings.Repeat("synthetic", 150000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/binary" {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(binary)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]string{"text": text}})
	}))
	defer server.Close()
	f, p, store := externalHTTPFixture(t, server.URL)
	output, err := p.callExternalHTTP(t.Context(), externalTestName, json.RawMessage(`{"input":"synthetic"}`))
	if err != nil || output.Text != text {
		t.Fatal("large JSON hit the private checkpoint budget", err)
	}
	setExternalBinaryDescriptor(t, f, p, server.URL+"/binary")
	for range 2 {
		output, err = p.callExternalHTTP(t.Context(), externalTestName, json.RawMessage(`{"input":"synthetic"}`))
		if err != nil || len(output.Media) != 1 || !bytes.Equal(output.Media[0].Data, binary) {
			t.Fatal("25MiB binary did not recover from host-only bytes", err)
		}
	}
	page, err := store.Jobs(t.Context(), externalHTTPPluginID, 0, 10)
	if err != nil || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
}

func TestLiveExternalHTTPInstalledLPACUsesSyntheticProvider(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in isolated external HTTP contract with local synthetic provider")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/binary" {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(bytes.Repeat([]byte{0x7f}, 25<<20))
			return
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-host-secret" {
			t.Error("host credential missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"text":"isolated synthetic provider output"}}`))
	}))
	defer server.Close()
	f, p := externalLegacyFixture(t, server.URL)
	store := installExternalHTTPFixture(t, p)
	python, _ = filepath.Abs(python)
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	c, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime, f.manager.packageRuntimeReady = c, true
	t.Cleanup(func() { _ = c.Close() })
	for range 2 {
		result, err := p.Call(t.Context(), externalTestName, json.RawMessage(`{"input":"synthetic"}`))
		if err != nil || !strings.Contains(result, "isolated synthetic provider output") {
			t.Fatal(result, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("isolated plugin repeated the provider request", calls.Load())
	}
	setExternalBinaryDescriptor(t, f, p, server.URL+"/binary")
	output, err := p.callExternalHTTP(t.Context(), externalTestName, json.RawMessage(`{"input":"synthetic"}`))
	if err != nil || len(output.Media) != 1 || len(output.Media[0].Data) != (25<<20) || output.Media[0].Data[0] != 0x7f {
		t.Fatal("isolated package did not preserve 25MiB binary via host ref", err)
	}
	if calls.Load() != 2 {
		t.Fatal("binary reference triggered an extra HTTP call", calls.Load())
	}

}
