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

func TestProjectReadFailureExplainsHowToChooseRegisteredProject(t *testing.T) {
	for code, want := range map[string]string{
		"project_not_registered":       "project_list",
		"project_manifest_unavailable": "manifests",
	} {
		if got := projectPluginResultError(code).Error(); !strings.Contains(got, want) || !strings.Contains(got, code) {
			t.Fatalf("%s guidance = %q", code, got)
		}
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

func TestProjectPluginPackagedToolIndexIsScopedAndDoesNotExposeControlPlane(t *testing.T) {
	_, p := trustedAgent(t)
	installProjectPluginTestRuntime(t, p)
	public := p.executionContext().Public
	media := filepath.Join(public, "runtimes", "windows-media")
	python := filepath.Join(public, "runtimes", "windows-python-cli")
	for _, fixture := range []struct {
		dir, binary, version string
	}{
		{media, "ffmpeg.exe", "synthetic ffmpeg 8"},
		{media, "ffprobe.exe", "synthetic ffmpeg 8"},
		{python, "python.exe", "3.13.synthetic"},
	} {
		if err := os.MkdirAll(fixture.dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.dir, fixture.binary), []byte("synthetic executable"), 0600); err != nil {
			t.Fatal(err)
		}
		manifest := filepath.Join(fixture.dir, "runtime-manifest.json")
		if _, err := os.Stat(manifest); os.IsNotExist(err) {
			data, _ := json.Marshal(map[string]any{"version": fixture.version, "files": map[string]string{fixture.binary: "synthetic"}})
			if err := os.WriteFile(manifest, data, 0600); err != nil {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}

	result, err := p.callProjectPlugin(t.Context(), "server_tools", json.RawMessage(`{"classes":["media"],"id":"media.ffmpeg"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result, "windows-music.json") || strings.Contains(result, "private") || strings.Contains(result, "spool") {
		t.Fatalf("control-plane path leaked from runtime index: %s", result)
	}
	var output struct {
		Tools []packagedTool `json:"tools"`
	}
	if json.Unmarshal([]byte(result), &output) != nil || len(output.Tools) != 1 {
		t.Fatalf("unexpected packaged tool index: %s", result)
	}
	tool := output.Tools[0]
	if tool.ID != "media.ffmpeg" || !tool.Available || tool.Version != "synthetic ffmpeg 8" || tool.Invocation != "server_windows_process" || !strings.Contains(tool.VersionCall, "ffmpeg.exe") || !strings.Contains(tool.Repeat, "request_key") {
		t.Fatalf("unexpected tool descriptor: %#v", tool)
	}
	if !strings.HasPrefix(tool.Path, media) {
		t.Fatalf("runtime tool escaped public runtime root: %q", tool.Path)
	}
	if _, err := p.callProjectPlugin(t.Context(), "server_tools", json.RawMessage(`{"classes":["control"]}`)); err == nil {
		t.Fatal("invalid tool class was accepted")
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
	if runtime.GOOS == "windows" {
		helper, _ := filepath.Abs("../../scripts/windows_agent_host.pyw")
		cfg := f.manager.agent.config()
		cfg.Host.Mode = "full"
		cfg.TasksEnabled = true
		cfg.Host.Windows = &agenthost.WindowsConfig{Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), Helper: helper, Spool: filepath.Join(f.dir, "synthetic-command-spool")}
		if err = f.manager.agent.save(cfg); err != nil {
			t.Fatal(err)
		}
		// Create both the tool and its public registration through normal Agent
		// tools. The tool's output cannot be mistaken for a literal echo command.
		indexed, err := p.callProjectPlugin(t.Context(), "server_tools", json.RawMessage(`{"id":"python.cli"}`))
		if err != nil {
			t.Fatal(err)
		}
		var indexedTools struct {
			Tools []packagedTool `json:"tools"`
		}
		if json.Unmarshal([]byte(indexed), &indexedTools) != nil || len(indexedTools.Tools) != 1 || !indexedTools.Tools[0].Available || indexedTools.Tools[0].Path == "" {
			t.Fatal("managed Python CLI was not discoverable through the normal tool index", indexed)
		}
		pythonCLI := indexedTools.Tools[0].Path
		toolPath := filepath.Join(created.Path, "self_tool.py")
		tool, _ := json.Marshal(map[string]string{"path": toolPath, "content": "print('self-authored-tool-draft')\n"})
		if _, err = p.callProjectPlugin(t.Context(), "server_write", tool); err != nil {
			t.Fatal(err)
		}
		toolRead, _ := json.Marshal(map[string]any{"path": toolPath, "length": 524288})
		result, err = p.callProjectPlugin(t.Context(), "server_read", toolRead)
		if err != nil {
			t.Fatal(err)
		}
		var beforeEdit struct {
			SHA256 string `json:"sha256"`
		}
		if json.Unmarshal([]byte(result), &beforeEdit) != nil || beforeEdit.SHA256 == "" || !strings.Contains(result, "self-authored-tool-draft") {
			t.Fatal("created tool cannot be read for a version-pinned edit", result)
		}
		edit, _ := json.Marshal(map[string]string{"path": toolPath, "expected_sha256": beforeEdit.SHA256, "old_text": "self-authored-tool-draft", "new_text": "self-authored-tool-output"})
		if _, err = p.callProjectPlugin(t.Context(), "server_edit", edit); err != nil {
			t.Fatal(err)
		}
		probePath := filepath.Join(created.Path, "read_outside.py")
		probe, _ := json.Marshal(map[string]string{"path": probePath, "content": "from pathlib import Path\nimport sys\nprint(Path(sys.argv[1]).read_text(encoding='utf-8'))\n"})
		if _, err = p.callProjectPlugin(t.Context(), "server_write", probe); err != nil {
			t.Fatal(err)
		}
		public, err := p.callProjectPlugin(t.Context(), "project_create", json.RawMessage(`{"name":"self-tool-public","shared":true}`))
		if err != nil {
			t.Fatal(err)
		}
		var publicProject struct{ Path string }
		if json.Unmarshal([]byte(public), &publicProject) != nil || !strings.HasPrefix(publicProject.Path, p.executionContext().Public) {
			t.Fatal("public project was not created in the public workspace", public)
		}
		if !strings.Contains(public, `"registered":true`) {
			t.Fatal("shared project did not receive its restricted public registration", public)
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
		selfStart, _ := json.Marshal(map[string]any{"action": "start", "path": created.Path, "argv": []string{pythonCLI, "self_tool.py"}, "request_key": "self-authored-tool-first-run", "timeout_seconds": 0})
		result, err = p.callProjectPlugin(t.Context(), "server_windows_process", selfStart)
		if err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal([]byte(result), &process) != nil || process.ID == "" {
			t.Fatal("self-authored tool did not return a process identity", result)
		}
		selfStatus, _ := json.Marshal(map[string]string{"action": "status", "id": process.ID})
		deadline = time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			result, err = p.callProjectPlugin(t.Context(), "server_windows_process", selfStatus)
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
		if process.State != "completed" || process.ExitCode == nil || *process.ExitCode != 0 || !strings.Contains(process.Output, "self-authored-tool-output") {
			t.Fatal("self-authored tool output was not produced by the written tool", result)
		}
		cancel, _ := json.Marshal(map[string]string{"action": "cancel", "id": identity})
		if result, err = p.callProjectPlugin(t.Context(), "server_windows_process", cancel); err != nil || !strings.Contains(result, identity) {
			t.Fatal("cancel lost original process identity", result, err)
		}

		// Rebuild the installed runtime, then use a newly-created normal Agent
		// access object. Discovery starts from server_status/server_list rather
		// than retaining the old absolute project path in an in-memory turn.
		if err = controller.Close(); err != nil {
			t.Fatal(err)
		}
		restarted, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: filepath.Join(filepath.Dir(helper), "windows_plugin_host.pyw"), RuntimeRoots: []string{filepath.Dir(python)}}))
		if err != nil {
			t.Fatal(err)
		}
		p.manager.packageMu.Lock()
		p.manager.packageRuntime, p.manager.packageRuntimeReady = restarted, true
		p.manager.packageMu.Unlock()
		t.Cleanup(func() { _ = restarted.Close() })
		next, _, err := f.manager.toolAccess()
		if err != nil {
			t.Fatal(err)
		}
		next.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "self-tool-turn-after-restart", Mode: "direct"})
		available := map[string]bool{}
		for _, item := range next.projectPluginTools(t.Context()) {
			available[item.Name] = true
		}
		for _, name := range []string{"project_list", "project_read", "server_status", "server_list", "server_read", "server_windows_process"} {
			if !available[name] {
				t.Fatal("restarted agent did not rediscover normal project tool", name)
			}
		}
		result, err = next.callProjectPlugin(t.Context(), "server_status", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		var workspaceStatus struct {
			Roots []string `json:"roots"`
		}
		if json.Unmarshal([]byte(result), &workspaceStatus) != nil || len(workspaceStatus.Roots) == 0 {
			t.Fatal("status did not provide a fresh managed root", result)
		}
		projectsRoot := filepath.Join(workspaceStatus.Roots[0], "projects")
		listing, _ := json.Marshal(map[string]any{"path": projectsRoot, "limit": 100})
		result, err = next.callProjectPlugin(t.Context(), "server_list", listing)
		if err != nil || !strings.Contains(result, "isolated-synthetic") {
			t.Fatal("restarted agent could not discover its private project directory", result, err)
		}
		reopenedProject := filepath.Join(projectsRoot, "isolated-synthetic")
		reopenedTool := filepath.Join(reopenedProject, "self_tool.py")
		read, _ = json.Marshal(map[string]any{"path": reopenedTool, "length": 524288})
		result, err = next.callProjectPlugin(t.Context(), "server_read", read)
		if err != nil || !strings.Contains(result, "self-authored-tool-output") {
			t.Fatal("restarted agent could not read its discovered tool", result, err)
		}
		result, err = next.callProjectPlugin(t.Context(), "project_read", json.RawMessage(`{"id":"self-tool-public"}`))
		if err != nil || !strings.Contains(result, `"entry":"projects/self-tool-public"`) {
			t.Fatal("public registration did not persist across runtime restart", result, err)
		}
		indexed, err = next.callProjectPlugin(t.Context(), "server_tools", json.RawMessage(`{"id":"python.cli"}`))
		if err != nil || json.Unmarshal([]byte(indexed), &indexedTools) != nil || len(indexedTools.Tools) != 1 || !indexedTools.Tools[0].Available || indexedTools.Tools[0].Path != pythonCLI {
			t.Fatal("restarted Agent could not rediscover the same managed Python CLI", indexed, err)
		}
		reopenedStart, _ := json.Marshal(map[string]any{"action": "start", "path": reopenedProject, "argv": []string{indexedTools.Tools[0].Path, "self_tool.py"}, "request_key": "self-authored-tool-after-restart", "timeout_seconds": 0})
		result, err = next.callProjectPlugin(t.Context(), "server_windows_process", reopenedStart)
		if err != nil || json.Unmarshal([]byte(result), &process) != nil || process.ID == "" {
			t.Fatal("restarted agent did not run discovered self-authored tool", result, err)
		}
		selfStatus, _ = json.Marshal(map[string]string{"action": "status", "id": process.ID})
		deadline = time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			result, err = next.callProjectPlugin(t.Context(), "server_windows_process", selfStatus)
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
		if process.State != "completed" || process.ExitCode == nil || *process.ExitCode != 0 || !strings.Contains(process.Output, "self-authored-tool-output") {
			t.Fatal("restarted self-authored tool did not produce its own output", result)
		}

		// Close the whole Manager, not just its Controller. The reopened Manager
		// reconstructs the package store and normal Agent access from disk; no old
		// project path or in-memory plugin session is reused.
		if _, err = f.manager.Save(ConfigInput{Config: f.manager.Config().Config}); err != nil {
			t.Fatal("failed to persist the synthetic account authorization before restart", err)
		}
		if err = f.manager.Close(); err != nil {
			t.Fatal(err)
		}
		freshManager, err := NewManager(f.dir, "", f.native)
		if err != nil {
			t.Fatal(err)
		}
		freshStore, err := freshManager.packageStore()
		if err != nil {
			t.Fatal(err)
		}
		freshController, err := pluginruntime.NewController(freshStore, pluginruntime.InstalledExecutor(freshStore, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: filepath.Join(filepath.Dir(helper), "windows_plugin_host.pyw"), RuntimeRoots: []string{filepath.Dir(python)}}))
		if err != nil {
			t.Fatal(err)
		}
		freshManager.packageMu.Lock()
		freshManager.packageRuntime, freshManager.packageRuntimeReady = freshController, true
		freshManager.packageMu.Unlock()
		f.manager = freshManager
		t.Cleanup(func() { _ = freshManager.Close() })
		fresh, _, err := freshManager.toolAccess()
		if err != nil {
			t.Fatal(err)
		}
		fresh.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "self-tool-turn-after-manager-restart", Mode: "direct"})
		result, err = fresh.callProjectPlugin(t.Context(), "server_status", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal([]byte(result), &workspaceStatus) != nil || len(workspaceStatus.Roots) == 0 {
			t.Fatal("fresh Manager did not expose an authorized workspace root", result)
		}
		freshProjects := filepath.Join(workspaceStatus.Roots[0], "projects")
		listing, _ = json.Marshal(map[string]any{"path": freshProjects, "limit": 100})
		result, err = fresh.callProjectPlugin(t.Context(), "server_list", listing)
		if err != nil || !strings.Contains(result, "isolated-synthetic") {
			t.Fatal("fresh Manager could not rediscover the private self-authored project", result, err)
		}
		freshProject := filepath.Join(freshProjects, "isolated-synthetic")
		indexed, err = fresh.callProjectPlugin(t.Context(), "server_tools", json.RawMessage(`{"id":"python.cli"}`))
		if err != nil || json.Unmarshal([]byte(indexed), &indexedTools) != nil || len(indexedTools.Tools) != 1 || !indexedTools.Tools[0].Available || indexedTools.Tools[0].Path != pythonCLI {
			t.Fatal("fresh Manager could not rediscover its managed Python CLI", indexed, err)
		}
		freshStart, _ := json.Marshal(map[string]any{"action": "start", "path": freshProject, "argv": []string{indexedTools.Tools[0].Path, "self_tool.py"}, "request_key": "self-authored-tool-after-manager-restart", "timeout_seconds": 0})
		result, err = fresh.callProjectPlugin(t.Context(), "server_windows_process", freshStart)
		if err != nil || json.Unmarshal([]byte(result), &process) != nil || process.ID == "" {
			t.Fatal("fresh Manager did not run rediscovered self-authored tool", result, err)
		}
		selfStatus, _ = json.Marshal(map[string]string{"action": "status", "id": process.ID})
		deadline = time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			result, err = fresh.callProjectPlugin(t.Context(), "server_windows_process", selfStatus)
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
		if process.State != "completed" || process.ExitCode == nil || *process.ExitCode != 0 || !strings.Contains(process.Output, "self-authored-tool-output") {
			t.Fatal("fresh Manager self-authored tool did not produce its own output", result)
		}

		// These are synthetic canaries only. The next calls must be refused by
		// normal tools before either file's content can reach the Agent process.
		canaries := []string{
			filepath.Join(f.dir, "dist", "data", "admin", "control-plane-canary.txt"),
			filepath.Join(f.manager.privateWorkspaceFor("synthetic-other-account"), "conversations", "other", "account-canary.txt"),
		}
		for _, canary := range canaries {
			if err = os.MkdirAll(filepath.Dir(canary), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(canary, []byte("synthetic-canary-never-readable"), 0600); err != nil {
				t.Fatal(err)
			}
			blockedRead, _ := json.Marshal(map[string]any{"path": canary, "length": 128})
			if leaked, e := fresh.callProjectPlugin(t.Context(), "server_read", blockedRead); e == nil || strings.Contains(leaked, "synthetic-canary-never-readable") {
				t.Fatal("normal Agent read escaped its workspace", canary, leaked, e)
			}
			blockedRun, _ := json.Marshal(map[string]any{"action": "start", "path": filepath.Dir(canary), "command": "type control-plane-canary.txt", "request_key": "blocked-" + filepath.Base(filepath.Dir(canary)), "timeout_seconds": 0})
			if leaked, e := fresh.callProjectPlugin(t.Context(), "server_windows_process", blockedRun); e == nil || strings.Contains(leaked, "synthetic-canary-never-readable") {
				t.Fatal("normal Agent process escaped its workspace", canary, leaked, e)
			}
			childRead, _ := json.Marshal(map[string]any{"action": "start", "path": freshProject, "argv": []string{pythonCLI, "read_outside.py", canary}, "request_key": "child-read-" + namedSpeechSHA([]byte(canary))[:16], "timeout_seconds": 0})
			result, err = fresh.callProjectPlugin(t.Context(), "server_windows_process", childRead)
			if err != nil || json.Unmarshal([]byte(result), &process) != nil || process.ID == "" {
				t.Fatal("authorized LPAC child did not start for boundary check", canary, result, err)
			}
			selfStatus, _ = json.Marshal(map[string]string{"action": "status", "id": process.ID})
			deadline = time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				result, err = fresh.callProjectPlugin(t.Context(), "server_windows_process", selfStatus)
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
			if process.State != "failed" || strings.Contains(process.Output, "synthetic-canary-never-readable") {
				t.Fatal("authorized LPAC child read an out-of-scope canary", canary, result)
			}
		}

		// A group member receives an independent private workspace, and cannot
		// recover the owner's tool by supplying its old directory path.
		f.manager.mu.Lock()
		f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "self-tool-members@chatroom", Mode: "mention"})
		f.manager.mu.Unlock()
		member, _, err := freshManager.toolAccess()
		if err != nil {
			t.Fatal(err)
		}
		member.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "self-tool-members@chatroom", Sender: "wxid_member", TraceID: "member-private-tool", Mode: "direct", IsGroup: true})
		memberProject, err := member.callProjectPlugin(t.Context(), "project_create", json.RawMessage(`{"name":"member-private-tool"}`))
		if err != nil || strings.Contains(memberProject, reopenedProject) {
			t.Fatal("ordinary member did not receive an independent private project", memberProject, err)
		}
		if _, err = member.callProjectPlugin(t.Context(), "server_read", read); err == nil {
			t.Fatal("ordinary member read the owner's private self-authored tool")
		}
	}

}
