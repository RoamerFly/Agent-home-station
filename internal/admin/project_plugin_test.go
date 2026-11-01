package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/companion"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

// Installation is explicit: ordinary trustedAgent fixtures have no optional
// project capability unless a test deliberately installs this safe package.
func installProjectPluginFixture(t *testing.T, p *permittedTools) *pluginhost.Store {
	t.Helper()
	s, err := p.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := s.Prepare(t.Context(), "../../plugins/project-development")
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveConfiguration(t.Context(), projectPluginID, pluginhost.ConfigUpdate{Values: map[string]json.RawMessage{}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetEnabled(t.Context(), projectPluginID, i.Revision, true); err != nil {
		t.Fatal(err)
	}
	return s
}

// A synthetic executor exercises Controller intent/CAS/service/private-result
// gates without launching commands, opening network connections or model calls.
// Python package behavior is tested separately by its fake-host unit tests and
// the opt-in actual isolated runtime test below.
type projectSyntheticExecutor struct{ p *permittedTools }

func (e *projectSyntheticExecutor) Start(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
	return e, nil
}
func (e *projectSyntheticExecutor) Health(context.Context) error { return nil }
func (e *projectSyntheticExecutor) Close() error                 { return nil }
func (e *projectSyntheticExecutor) Call(ctx context.Context, _ pluginhost.Invocation, _ string, raw json.RawMessage) (json.RawMessage, error) {
	var request projectPluginRequest
	if json.Unmarshal(raw, &request) != nil {
		return nil, errors.New("synthetic invalid request")
	}
	// Synthetic validation mirrors only the fixture input boundary. Actual
	// package validation has independent Python and isolated-runtime coverage.
	if request.Tool == "project_create" {
		var input struct{ Name string }
		_ = json.Unmarshal(request.Arguments, &input)
		if !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`).MatchString(input.Name) || regexp.MustCompile(`^(con|prn|aux|nul|com[0-9]|lpt[0-9])$`).MatchString(input.Name) {
			return json.RawMessage(`{"error":"invalid_project_name"}`), nil
		}
	}
	rows, _, _, err := e.p.projectPluginCatalog(ctx)
	if err != nil {
		return nil, err
	}
	operation := ""
	for _, row := range rows {
		if row.Name == request.Tool {
			operation = row.Operation
		}
	}
	args, _ := json.Marshal(map[string]any{"tool": request.Tool, "operation": operation, "arguments": request.Arguments})
	observed, err := pluginruntime.InvokeHostService(ctx, "wechat/workspace_request", args)
	if err != nil {
		return nil, err
	}
	var observation projectPluginObservation
	if json.Unmarshal(observed, &observation) != nil {
		return nil, errors.New("synthetic invalid observation")
	}
	result := projectPluginResult{Observation: observation, Value: observation.Value, Error: observation.Error}
	if operation == "snapshot" && observation.Error == "" {
		items := []json.RawMessage{}
		issues := []projectManifestIssue{}
		for _, record := range observation.Records {
			reason := record.Error
			var object map[string]json.RawMessage
			if reason == "" {
				if record.TooLarge {
					reason = "project_manifest_metadata_too_large"
				} else if json.Unmarshal([]byte(record.Content), &object) != nil || object == nil {
					reason = "project_manifest_invalid"
				}
			}
			if reason != "" {
				issues = append(issues, projectManifestIssue{Name: record.Name, Reason: reason})
			} else {
				items = append(items, json.RawMessage(record.Content))
			}
		}
		if request.Tool == "project_read" {
			if len(items) == 1 {
				result.Value = items[0]
			} else {
				result.Error = "project_not_registered"
				if len(issues) > 0 {
					result.Error = issues[0].Reason
				}
			}
		} else {
			result.Value, _ = json.Marshal(map[string]any{"projects": items, "unavailable_manifests": issues, "execution": observation.Execution})
		}
	}
	if len(result.Value) == 0 {
		result.Value = json.RawMessage("null")
	}
	return json.Marshal(result)
}

func installProjectPluginTestRuntime(t *testing.T, p *permittedTools) *pluginhost.Store {
	t.Helper()
	s := installProjectPluginFixture(t, p)
	c, err := pluginruntime.NewController(s, &projectSyntheticExecutor{p: p})
	if err != nil {
		t.Fatal(err)
	}
	p.manager.packageRuntime, p.manager.packageRuntimeReady = c, true
	t.Cleanup(func() { _ = c.Close() })
	return s
}

func projectPolicyJob(t *testing.T, p *permittedTools, name string, raw json.RawMessage) (*projectPluginPolicy, pluginhost.Job, pluginhost.Capability) {
	t.Helper()
	policy, err := p.newProjectPluginPolicy(t.Context(), name, raw)
	if err != nil {
		t.Fatal(err)
	}
	j, _, err := policy.store.CreateJob(t.Context(), pluginhost.Job{Plugin: projectPluginID, Version: policy.installed.Package.Version, PackageDigest: policy.installed.Digest, ConfigRevision: "1", Tool: projectPluginCapability(policy.tool.Permission), Context: policy.invocation, Arguments: policy.arguments})
	if err != nil {
		t.Fatal(err)
	}
	j, err = policy.store.ClaimJob(t.Context(), j.ID, j.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return policy, j, pluginhost.Capability{ID: projectPluginCapability(policy.tool.Permission), Permissions: []string{policy.tool.Permission}, Implementation: "ready", Discoverable: true}
}

func projectBrokerRequest(policy *projectPluginPolicy) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"tool": policy.request.Tool, "operation": policy.tool.Operation, "arguments": policy.request.Arguments})
	return raw
}

func TestProjectPluginMissingHasNoCoreFallbackAndKeepsNotes(t *testing.T) {
	_, p := trustedAgent(t)
	if tools := p.projectPluginTools(t.Context()); len(tools) != 0 {
		t.Fatal("uninstalled package exposed tools", tools)
	}
	for _, tool := range p.agentTools() {
		if isProjectPluginTool(tool.Name) {
			t.Fatal("core catalog exposed an uninstalled project capability", tool.Name)
		}
	}
	for _, name := range []string{"server_status", "project_list"} {
		if _, err := p.Call(t.Context(), name, json.RawMessage(`{}`)); err == nil {
			t.Fatal("central dispatcher retained a core project fallback", name)
		}
	}
	for _, name := range []string{"project_create", "project_list", "project_read", "server_status", "server_list", "server_read", "server_search", "server_write", "server_edit", "server_exec", "server_process", "server_windows_process"} {
		if _, err := p.callProjectPlugin(t.Context(), name, json.RawMessage(`{}`)); err == nil {
			t.Fatal("uninstalled project fallback", name)
		}
	}
	if _, err := p.Call(t.Context(), "workspace_read", json.RawMessage(`{"name":"self-tools.md","shared":true}`)); err != nil {
		t.Fatal("core note memory depends on optional plugin", err)
	}
}

func TestProjectPluginToolPermissionsAndPlatform(t *testing.T) {
	f, p := trustedAgent(t)
	installProjectPluginFixture(t, p)
	tools := p.projectPluginTools(t.Context())
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["server_read"] || !names["server_write"] || !names["project_create"] || names["server_exec"] {
		t.Fatal(names)
	}
	cfg := f.manager.agent.config()
	cfg.Host.Mode = "read"
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	for _, tool := range p.projectPluginTools(t.Context()) {
		if tool.Name == "server_write" || tool.Name == "project_create" || strings.Contains(tool.Name, "process") || tool.Name == "server_exec" {
			t.Fatal("read-only exposed mutation", tool.Name)
		}
	}
	if p.projectPluginToolAllowed(projectPluginTool{Name: "server_read", Permission: "workspace.write", Operation: "write"}, []string{"workspace.read", "workspace.write"}) {
		t.Fatal("package relabelled a read as write")
	}
}

func TestProjectPluginBrokerBindsOriginalRequestAndActualResult(t *testing.T) {
	_, p := trustedAgent(t)
	installProjectPluginFixture(t, p)
	file := filepath.Join(p.executionContext().Conversation, "scratch", "bound.txt")
	raw, _ := json.Marshal(map[string]string{"path": file, "content": "synthetic exact bytes"})
	policy, j, cap := projectPolicyJob(t, p, "server_write", raw)
	changed := json.RawMessage(`{"tool":"server_write","operation":"write","arguments":{"path":"elsewhere","content":"different"}}`)
	if _, err := policy.Service(t.Context(), j, cap, "wechat/workspace_request", changed); err == nil {
		t.Fatal("changed IO arguments accepted")
	}
	observed, err := policy.Service(t.Context(), j, cap, "wechat/workspace_request", projectBrokerRequest(policy))
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(file); string(data) != "synthetic exact bytes" {
		t.Fatal("host did not write actual requested bytes")
	}
	if _, err := policy.Service(t.Context(), j, cap, "wechat/workspace_request", projectBrokerRequest(policy)); err == nil {
		t.Fatal("host effect replayed")
	}
	var observation projectPluginObservation
	_ = json.Unmarshal(observed, &observation)
	result := projectPluginResult{Observation: observation, Value: observation.Value}
	output, _ := json.Marshal(result)
	verified, err := policy.Validate(t.Context(), j, cap, output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(verified.Result), "synthetic exact bytes") || len(verified.Private) == 0 {
		t.Fatal("private file data in public checkpoint")
	}
	result.Value = json.RawMessage(`{"state":"completed","path":"fake path"}`)
	output, _ = json.Marshal(result)
	if _, err := policy.Validate(t.Context(), j, cap, output); err == nil {
		t.Fatal("plugin invented successful IO")
	}
}

func TestProjectPluginBrokerRejectsScopeAndCancellationChanges(t *testing.T) {
	_, p := trustedAgent(t)
	installProjectPluginFixture(t, p)
	policy, j, cap := projectPolicyJob(t, p, "server_status", json.RawMessage(`{}`))
	original := j.Context.Account
	j.Context.Account = "another-account"
	if _, err := policy.Service(t.Context(), j, cap, "wechat/workspace_request", projectBrokerRequest(policy)); err == nil {
		t.Fatal("cross-account request accepted")
	}
	j.Context.Account = original
	current, err := policy.store.RequestJobCancel(t.Context(), j.ID, j.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.Service(t.Context(), current, cap, "wechat/workspace_request", projectBrokerRequest(policy)); err == nil {
		t.Fatal("cancelled job used host broker")
	}
}

func TestProjectPluginAuthoritySnapshotDetachesMutableProofAndKeepsAuditFields(t *testing.T) {
	originalText := "raw owner request"
	original := companion.Input{
		Account: "test", Thread: "owner", Sender: "owner", TraceID: "raw-trace", QuoteServerID: "raw-quote", OriginalText: &originalText,
		Preview: true, MustReply: true, HistoryObserved: true, PersonaOverride: "host-only persona", UnreadMediaIDs: []string{"host-only-media"},
		Source:       &companion.SourceRef{Account: "test", Sender: "owner", BodyHash: "original-body-proof"},
		ReplyContext: &companion.ReplyContext{SourceVersions: map[string]int{"original-event": 3}, TargetIDs: []string{"original-target"}},
	}
	cloned, err := cloneProjectPluginTrace(original)
	if err != nil {
		t.Fatal(err)
	}
	original.Source.Sender = "attacker"
	original.ReplyContext.SourceVersions["original-event"] = 99
	original.ReplyContext.TargetIDs[0] = "attacker-target"
	original.UnreadMediaIDs[0] = "attacker-media"
	originalText = "mutated text"
	if cloned.Source.Sender != "owner" || cloned.Source.BodyHash != "original-body-proof" || cloned.ReplyContext.SourceVersions["original-event"] != 3 || cloned.ReplyContext.TargetIDs[0] != "original-target" || *cloned.OriginalText != "raw owner request" || cloned.UnreadMediaIDs[0] != "host-only-media" {
		t.Fatal("mutable source proof escaped the authorization snapshot", cloned)
	}
	if cloned.TraceID != "raw-trace" || cloned.QuoteServerID != "raw-quote" || !cloned.Preview || !cloned.MustReply || !cloned.HistoryObserved || cloned.PersonaOverride != "host-only persona" {
		t.Fatal("raw audit or owner restrictions were lost", cloned)
	}
}

func TestProjectPluginPreservesOnlyHostVerifiedRuntimeFailureAndAllowsRejectedRetry(t *testing.T) {
	f, p := trustedAgent(t)
	store := installProjectPluginTestRuntime(t, p)
	cfg := f.manager.agent.config()
	cfg.Host.Mode = "full"
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(p.executionContext().Conversation, "scratch", "repairable-missing")
	args, _ := json.Marshal(map[string]any{"action": "start", "path": dir, "command": "echo synthetic-repair", "request_key": "safe-rejected-retry"})
	_, err := p.callProjectPlugin(t.Context(), "server_process", args)
	var failure *RuntimeFailure
	if !errors.As(err, &failure) || !failure.NotStarted || failure.Code == "" || failure.Stage == "" || failure.Continuation == "" {
		t.Fatal("host RuntimeFailure was flattened", err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	result, err := p.callProjectPlugin(t.Context(), "server_process", args)
	if err != nil || !strings.Contains(result, `"id":"process-`) {
		t.Fatal("safe admission rejection permanently froze the request key", result, err)
	}
	f.manager.processes.Close()
	page, err := store.Jobs(t.Context(), projectPluginID, 0, 10)
	if err != nil || len(page.Items) != 2 {
		t.Fatal("rejected admission was replayed from a terminal broker checkpoint", page, err)
	}
	// A package cannot invent a not-started envelope after actual host I/O.
	policy, job, cap := projectPolicyJob(t, p, "server_status", json.RawMessage(`{}`))
	observed, err := policy.Service(t.Context(), job, cap, "wechat/workspace_request", projectBrokerRequest(policy))
	if err != nil {
		t.Fatal(err)
	}
	var observation projectPluginObservation
	_ = json.Unmarshal(observed, &observation)
	observation.Failure = &RuntimeFailure{Code: "invented", Message: "invented", NotStarted: true}
	output, _ := json.Marshal(projectPluginResult{Observation: observation, Value: observation.Value})
	if _, err := policy.Validate(t.Context(), job, cap, output); err == nil {
		t.Fatal("plugin invented a trusted RuntimeFailure")
	}
}

func TestProjectPluginControllerPrivateCheckpointAndNoReplay(t *testing.T) {
	_, p := trustedAgent(t)
	store := installProjectPluginTestRuntime(t, p)
	file := filepath.Join(p.executionContext().Conversation, "scratch", "controller.txt")
	args, _ := json.Marshal(map[string]string{"path": file, "content": "synthetic checkpoint bytes"})
	first, err := p.callProjectPlugin(t.Context(), "server_write", args)
	if err != nil {
		t.Fatal(err)
	}
	again, err := p.callProjectPlugin(t.Context(), "server_write", args)
	if err != nil || !sameJSON([]byte(first), []byte(again)) {
		t.Fatal("same request replayed or lost checkpoint", first, again, err)
	}
	page, err := store.Jobs(t.Context(), projectPluginID, 0, 10)
	if err != nil || len(page.Items) != 1 || strings.Contains(string(page.Items[0].Result), "checkpoint bytes") {
		t.Fatal("unsafe public checkpoint", page, err)
	}
}

func TestProjectPluginAdminRegistryIsSessionBoundAndReadOnly(t *testing.T) {
	f, p := trustedAgent(t)
	store := installProjectPluginTestRuntime(t, p)
	manifest := filepath.Join(p.executionContext().Public, "manifests", "console.json")
	if err := os.WriteFile(manifest, []byte(`{"id":"console","name":"Synthetic console registration"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.request("GET", "/api/agent/projects", nil, false, false); code != 401 {
		t.Fatal("unauthenticated registry inspection", code)
	}
	f.login()
	code, raw, _ := f.request("GET", "/api/agent/projects", nil, true, false)
	if code != 200 || !strings.Contains(string(raw), "Synthetic console registration") {
		t.Fatal("authenticated registry route did not use installed package", code, string(raw))
	}
	page, err := store.Jobs(t.Context(), projectPluginID, 0, 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].Context.Thread != "host:project-console" || page.Items[0].Context.Requester != "administrator" || len(page.Items[0].Context.SourceVersions) != 0 {
		t.Fatal("console fabricated a chat source or actor", page, err)
	}
	valid := true
	authority := &speechAdminTestAuthority{Account: p.trace.Account, Session: "synthetic-session-fingerprint", RequestID: "synthetic-console-request", Check: func(context.Context) error {
		if !valid {
			return errors.New("synthetic session revoked")
		}
		return nil
	}}
	ctx := context.WithValue(t.Context(), speechAdminTestContextKey{}, authority)
	if _, err := p.newProjectPluginPolicy(ctx, "server_write", json.RawMessage(`{"path":"anywhere","content":"forbidden"}`)); err == nil {
		t.Fatal("console registry authority became file write authority")
	}
	policy, err := p.newProjectPluginPolicy(ctx, "project_list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	job := pluginhost.Job{Plugin: projectPluginID, Tool: projectPluginCapability(policy.tool.Permission), Context: policy.invocation, Arguments: policy.arguments}
	if err := policy.Check(ctx, job, pluginhost.Capability{}); err != nil {
		t.Fatal(err)
	}
	valid = false
	if err := policy.Check(ctx, job, pluginhost.Capability{}); err == nil {
		t.Fatal("revoked console session retained registry access")
	}
}

func TestLiveProjectPluginIsolatedRuntimeWithSyntheticWorkspace(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in offline installed-package isolation test")
	}
	f, p := trustedAgent(t)
	store := installProjectPluginFixture(t, p)
	python, _ = filepath.Abs(python)
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	controller, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	p.manager.packageRuntime, p.manager.packageRuntimeReady = controller, true
	t.Cleanup(func() { _ = controller.Close() })
	result, err := p.callProjectPlugin(t.Context(), "project_create", json.RawMessage(`{"name":"isolated-synthetic"}`))
	if err != nil {
		t.Fatal(err)
	}
	var created struct{ Path string }
	if json.Unmarshal([]byte(result), &created) != nil || !strings.HasPrefix(created.Path, p.executionContext().Conversation) {
		t.Fatal(result)
	}
	args, _ := json.Marshal(map[string]string{"path": filepath.Join(created.Path, "README.md"), "content": "synthetic installed package evidence"})
	if _, err = p.callProjectPlugin(t.Context(), "server_write", args); err != nil {
		t.Fatal(err)
	}
	read, _ := json.Marshal(map[string]any{"path": filepath.Join(created.Path, "README.md"), "length": 524288})
	if result, err = p.callProjectPlugin(t.Context(), "server_read", read); err != nil || !strings.Contains(result, "synthetic installed package evidence") {
		t.Fatal(result, err)
	}
	manifest := filepath.Join(p.executionContext().Public, "manifests", "synthetic.json")
	if err = os.WriteFile(manifest, []byte(`{"id":"synthetic","name":"Isolated package registration"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err = p.callProjectPlugin(t.Context(), "project_list", json.RawMessage(`{}`)); err != nil || !strings.Contains(result, "Isolated package registration") {
		t.Fatal(result, err)
	}
	if runtime.GOOS == "windows" {
		helper, _ := filepath.Abs("../../scripts/windows_agent_host.pyw")
		cfg := f.manager.agent.config()
		cfg.Host.Mode = "full"
		cfg.TasksEnabled = true
		cfg.Host.Windows = &agenthost.WindowsConfig{Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), Helper: helper, Spool: filepath.Join(f.dir, "synthetic-command-spool")}
		if err = f.manager.agent.save(cfg); err != nil {
			t.Fatal(err)
		}
		start, _ := json.Marshal(map[string]any{"action": "start", "path": created.Path, "command": "echo project-plugin-cpu-echo", "request_key": "isolated-echo-only", "timeout_seconds": 0})
		result, err = p.callProjectPlugin(t.Context(), "server_windows_process", start)
		if err != nil {
			t.Fatal(err)
		}
		var process struct {
			ID, State, Output string
			ExitCode          *int `json:"exit_code"`
		}
		if json.Unmarshal([]byte(result), &process) != nil || process.ID == "" {
			t.Fatal("no durable isolated process identity", result)
		}
		identity := process.ID
		repeated, err := p.callProjectPlugin(t.Context(), "server_windows_process", start)
		if err != nil {
			t.Fatal(err)
		}
		var repeatedProcess struct{ ID string }
		_ = json.Unmarshal([]byte(repeated), &repeatedProcess)
		if repeatedProcess.ID != identity {
			t.Fatal("request key launched a second process", result, repeated)
		}
		status, _ := json.Marshal(map[string]string{"action": "status", "id": identity})
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			result, err = p.callProjectPlugin(t.Context(), "server_windows_process", status)
			if err != nil {
				t.Fatal(err)
			}
			if json.Unmarshal([]byte(result), &process) != nil {
				t.Fatal(result)
			}
			if process.State != "running" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if process.State != "completed" || process.ExitCode == nil || *process.ExitCode != 0 || !strings.Contains(process.Output, "project-plugin-cpu-echo") {
			t.Fatal("isolated CPU echo did not complete", result)
		}
		cancel, _ := json.Marshal(map[string]string{"action": "cancel", "id": identity})
		if result, err = p.callProjectPlugin(t.Context(), "server_windows_process", cancel); err != nil || !strings.Contains(result, identity) {
			t.Fatal("cancel lost original process identity", result, err)
		}
	}

}
