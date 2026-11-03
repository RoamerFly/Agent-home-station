package admin

// Project business metadata and registration interpretation live in the
// installed package. This adapter holds only authenticated workspace authority,
// exact-request mediation and host-observed evidence.
import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/workspacefs"
)

const projectPluginID = "project-development"

func projectPluginCapability(permission string) string {
	return map[string]string{"workspace.read": "project.workspace_read", "workspace.write": "project.workspace_write", "process.execute": "project.workspace_execute"}[permission]
}

type projectPluginTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"input_schema"`
	Permission  string          `json:"permission"`
	Operation   string          `json:"operation"`
}
type projectPluginRequest struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}
type projectPluginObservation struct {
	Value     json.RawMessage       `json:"value,omitempty"`
	Error     string                `json:"error,omitempty"`
	Failure   *RuntimeFailure       `json:"safe_failure,omitempty"`
	Records   []projectPluginRecord `json:"records"`
	Execution *ExecutionContext     `json:"execution,omitempty"`
}
type projectPluginRecord struct {
	Name     string `json:"name"`
	Content  string `json:"content,omitempty"`
	Error    string `json:"error,omitempty"`
	TooLarge bool   `json:"too_large,omitempty"`
}
type projectPluginResult struct {
	Observation projectPluginObservation `json:"observation"`
	Value       json.RawMessage          `json:"value"`
	Error       string                   `json:"error"`
}

func isProjectPluginTool(name string) bool {
	return slices.Contains([]string{"project_list", "project_read", "project_create", "server_status", "server_list", "server_read", "server_search", "server_write", "server_edit", "server_exec", "server_process", "server_windows_process"}, name)
}

func (p *permittedTools) projectPluginCatalog(ctx context.Context) ([]projectPluginTool, *pluginhost.Store, pluginhost.InstalledPackage, error) {
	var installed pluginhost.InstalledPackage
	s, err := p.manager.packageStore()
	if err != nil {
		return nil, nil, installed, err
	}
	installed, err = s.Installed(ctx, projectPluginID)
	if err != nil || !installed.Enabled || installed.Removed || s.CheckConfiguration(ctx, projectPluginID) != nil {
		return nil, s, installed, errors.New("project_plugin_required")
	}
	dir, err := s.PackageDirectory(installed.Digest)
	if err != nil {
		return nil, s, installed, err
	}
	if _, err = pluginhost.Verify(dir); err != nil {
		return nil, s, installed, err
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil || namedSpeechSHA(manifest) != installed.Digest {
		return nil, s, installed, errors.New("project_plugin_integrity_changed")
	}
	data, err := os.ReadFile(filepath.Join(dir, "runtime", "catalog.json"))
	if err != nil || namedSpeechSHA(data) != installed.Package.Files["runtime/catalog.json"] {
		return nil, s, installed, errors.New("project_plugin_catalog_changed")
	}
	var catalog struct {
		Version int                 `json:"version"`
		Tools   []projectPluginTool `json:"tools"`
	}
	if musicStrictJSON(data, &catalog) != nil || catalog.Version != 1 {
		return nil, s, installed, errors.New("project_plugin_catalog_invalid")
	}
	caps, err := s.Capabilities(ctx, projectPluginID)
	if err != nil {
		return nil, s, installed, err
	}
	available := map[string]bool{}
	for _, cap := range caps {
		if cap.Implementation != "ready" || !cap.Discoverable || cap.Async {
			continue
		}
		for _, permission := range []string{"workspace.read", "workspace.write", "process.execute"} {
			if cap.ID == projectPluginCapability(permission) && slices.Equal(cap.Permissions, []string{permission}) {
				available[permission] = true
			}
		}
	}
	rows := []projectPluginTool{}
	for _, row := range catalog.Tools {
		if available[row.Permission] {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil, s, installed, errors.New("project_plugin_required")
	}
	return rows, s, installed, nil
}

func (p *permittedTools) projectPluginToolAllowed(tool projectPluginTool, grants []string) bool {
	if !isProjectPluginTool(tool.Name) || !p.workspaceExecutionAllowed() || !slices.Contains(grants, tool.Permission) {
		return false
	}
	cfg := p.manager.agent.config().Host
	// A package may describe and map an operation, but cannot relabel a read
	// call as a mutation or execute a different host primitive.
	operation := strings.TrimPrefix(tool.Name, "server_")
	if tool.Name == "project_list" || tool.Name == "project_read" {
		operation = "snapshot"
	}
	if tool.Name == "project_create" {
		operation = "mkdir"
	}
	permission := "workspace.read"
	if slices.Contains([]string{"project_create", "server_write", "server_edit"}, tool.Name) {
		permission = "workspace.write"
	}
	if slices.Contains([]string{"server_exec", "server_process", "server_windows_process"}, tool.Name) {
		permission = "process.execute"
	}
	if tool.Operation != operation || tool.Permission != permission {
		return false
	}
	switch tool.Permission {
	case "workspace.read":
		return cfg.Mode != "disabled" && slices.Contains([]string{"status", "list", "read", "search", "snapshot"}, tool.Operation)
	case "workspace.write":
		return (cfg.Mode == "files" || cfg.Mode == "full") && slices.Contains([]string{"mkdir", "write", "edit"}, tool.Operation)
	case "process.execute":
		return cfg.Mode == "full" && ((cfg.Windows != nil && tool.Name == "server_windows_process" && tool.Operation == "windows_process") || (cfg.Windows == nil && (tool.Name == "server_exec" && tool.Operation == "exec" || tool.Name == "server_process" && tool.Operation == "process")))
	}
	return false
}

func (p *permittedTools) projectPluginTools(ctx context.Context) []mcptools.Tool {
	rows, _, installed, err := p.projectPluginCatalog(ctx)
	if err != nil {
		return nil
	}
	result := []mcptools.Tool{}
	for _, row := range rows {
		if p.projectPluginToolAllowed(row, installed.Permissions) {
			result = append(result, mcptools.Tool{Name: row.Name, Description: row.Description, InputSchema: row.Schema})
		}
	}
	return result
}

type projectPluginPolicy struct {
	p              *permittedTools
	store          *pluginhost.Store
	installed      pluginhost.InstalledPackage
	configRevision int
	tool           projectPluginTool
	request        projectPluginRequest
	arguments      json.RawMessage
	invocation     pluginhost.Invocation
	authorization  string
	admin          *speechAdminTestAuthority
	mu             sync.Mutex
	requested      bool
	observed       json.RawMessage
}

func (p *permittedTools) projectPluginAuthorization(raw json.RawMessage) string {
	if !p.workspaceExecutionAllowed() || !p.manager.agentTaskActive(p.trace) {
		return ""
	}
	cfg, err := p.scopedHostConfig(raw)
	if err != nil {
		return ""
	}
	b, _ := json.Marshal([]any{p.trace, p.privileged(), p.manager.agent.config(), cfg})
	return namedSpeechSHA(b)
}

// Management inspection is session-bound and has no invented WeChat actor.
// Its authority is restricted to public registration snapshots only.
func projectPluginAdminToolAllowed(tool projectPluginTool, grants []string) bool {
	return (tool.Name == "project_list" || tool.Name == "project_read") && tool.Permission == "workspace.read" && tool.Operation == "snapshot" && slices.Contains(grants, "workspace.read")
}
func (p *projectPluginPolicy) adminAuthorization() string {
	return agentID(p.admin.Account, p.admin.Session, p.admin.RequestID, p.request.Tool, namedSpeechSHA(p.request.Arguments))
}

// Input contains audit-only fields excluded from JSON. Restore those after
// decoding into a fresh value so every mutable source/reply proof is detached
// without losing the raw trace, quote identity or owner/preview restrictions.
func cloneProjectPluginTrace(input companion.Input) (companion.Input, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return companion.Input{}, err
	}
	var trace companion.Input
	if err = json.Unmarshal(data, &trace); err != nil {
		return companion.Input{}, err
	}
	trace.UnreadMediaIDs = append([]string(nil), input.UnreadMediaIDs...)
	trace.HistoryObserved = input.HistoryObserved
	trace.PersonaOverride = input.PersonaOverride
	trace.Preview = input.Preview
	trace.MustReply = input.MustReply
	trace.TraceID = input.TraceID
	trace.QuoteServerID = input.QuoteServerID
	return trace, nil
}

func (p *permittedTools) newProjectPluginPolicy(ctx context.Context, name string, raw json.RawMessage) (*projectPluginPolicy, error) {
	trace, err := cloneProjectPluginTrace(p.trace)
	if err != nil {
		return nil, err
	}
	// Do not copy per-session mutexes or mutable media state into a long-lived
	// authority. Only the host manager and independently cloned request proof
	// are needed by workspace admission, process continuation and diagnostics.
	p = &permittedTools{manager: p.manager, trace: trace, privateDiagnostics: p.privateDiagnostics}
	rows, store, installed, err := p.projectPluginCatalog(ctx)
	if err != nil {
		return nil, errors.New("project_plugin_required")
	}
	policy := &projectPluginPolicy{p: p, store: store, installed: installed, request: projectPluginRequest{Tool: name, Arguments: append(json.RawMessage(nil), raw...)}}
	policy.admin, _ = ctx.Value(speechAdminTestContextKey{}).(*speechAdminTestAuthority)
	for _, row := range rows {
		if row.Name == name {
			policy.tool = row
			break
		}
	}
	if policy.admin != nil {
		if !projectPluginAdminToolAllowed(policy.tool, installed.Permissions) {
			return nil, errors.New("project_plugin_admin_read_only")
		}
		if err = policy.admin.valid(ctx, p.manager); err != nil {
			return nil, err
		}
		policy.authorization = policy.adminAuthorization()
	} else {
		if !p.projectPluginToolAllowed(policy.tool, installed.Permissions) {
			return nil, errors.New("project_plugin_permission_denied")
		}
		policy.authorization = p.projectPluginAuthorization(raw)
	}
	if policy.authorization == "" {
		return nil, errors.New("project_plugin_scope_denied")
	}
	config, err := store.Configuration(ctx, projectPluginID)
	if err != nil {
		return nil, err
	}
	policy.configRevision = config.Revision
	epoch, err := p.manager.memory.RecallEpoch(ctx)
	if err != nil {
		return nil, err
	}
	sources := map[string]int{}
	if p.trace.Source != nil && p.trace.Source.EventID != "" {
		sources[p.trace.Source.EventID] = p.trace.Source.Revision
	}
	if p.trace.ReplyContext != nil {
		for id, revision := range p.trace.ReplyContext.SourceVersions {
			sources[id] = revision
		}
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	// Background request_key remains in the original arguments and is enforced
	// by processStore across turns/restarts. Each observation gets a fresh job;
	// a retry may inspect that same durable process but never starts it twice.
	key := "workspace-" + hex.EncodeToString(nonce[:])
	if name == "server_exec" || name == "server_write" || name == "server_edit" {
		key = "workspace-effect-" + agentID(p.trace.Account, p.trace.Thread, p.trace.Sender, p.trace.TraceID, p.trace.TaskID, name, namedSpeechSHA(raw))
	}
	policy.invocation = pluginhost.Invocation{Account: p.trace.Account, Thread: p.trace.Thread, Requester: p.serviceRequester(), Purpose: "authorized workspace " + name, RequestKey: key, Authorization: policy.authorization, EvidenceEpoch: epoch, SourceVersions: sources}
	if policy.admin != nil {
		policy.invocation = pluginhost.Invocation{Account: policy.admin.Account, Thread: "host:project-console", Requester: "administrator", Purpose: "admin public project registry inspection", RequestKey: key, Authorization: policy.authorization}
	}
	policy.arguments, _ = json.Marshal(policy.request)
	return policy, nil
}

func (p *projectPluginPolicy) Check(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	currentAuthorization := p.p.projectPluginAuthorization(p.request.Arguments)
	if p.admin != nil {
		if !projectPluginAdminToolAllowed(p.tool, p.installed.Permissions) {
			return errors.New("project_plugin_admin_read_only")
		}
		if err := p.admin.valid(ctx, p.p.manager); err != nil {
			return err
		}
		currentAuthorization = p.adminAuthorization()
	}
	if j.Plugin != projectPluginID || j.Tool != projectPluginCapability(p.tool.Permission) || cap.Async || cap.ID != "" && cap.ID != projectPluginCapability(p.tool.Permission) || !reflect.DeepEqual(j.Context, p.invocation) || !sameJSON(j.Arguments, p.arguments) || p.authorization == "" || currentAuthorization != p.authorization {
		return errors.New("project_plugin_authorization_changed")
	}
	_, s, installed, err := p.p.projectPluginCatalog(ctx)
	if err != nil || installed.Digest != p.installed.Digest || installed.Revision != p.installed.Revision || !reflect.DeepEqual(installed.Permissions, p.installed.Permissions) || !(p.admin != nil && projectPluginAdminToolAllowed(p.tool, installed.Permissions) || p.admin == nil && p.p.projectPluginToolAllowed(p.tool, installed.Permissions)) {
		return errors.New("project_plugin_package_changed")
	}
	config, err := s.Configuration(ctx, projectPluginID)
	if err != nil || config.Revision != p.configRevision || j.PackageDigest != "" && (j.PackageDigest != installed.Digest || j.Version != installed.Package.Version || j.ConfigRevision != strconv.Itoa(config.Revision)) {
		return errors.New("project_plugin_configuration_changed")
	}
	if err = pluginhost.Authorize(installed.Package, pluginhost.Grant{Account: p.invocation.Account, Plugin: projectPluginID, Version: installed.Package.Version, Permissions: installed.Permissions}, j.Context, []string{p.tool.Permission}); err != nil {
		return err
	}
	epoch, err := p.p.manager.memory.RecallEpoch(ctx)
	if p.admin != nil {
		epoch = ""
	}
	if err != nil || epoch != p.invocation.EvidenceEpoch {
		return errors.New("project_plugin_evidence_changed")
	}
	for id, revision := range p.invocation.SourceVersions {
		ev, e := p.p.manager.memory.ReadEvent(ctx, []string{companion.ScopeID("wechat", p.invocation.Account, p.invocation.Thread)}, id)
		if e != nil || ev.Revision != revision {
			return errors.New("project_plugin_source_changed")
		}
		if source := p.p.trace.Source; source != nil && source.EventID == id && (ev.Role != "user" || ev.Sender != p.p.trace.Sender) {
			return errors.New("project_plugin_source_actor_changed")
		}
	}
	if source := p.p.trace.Source; source != nil && (source.Integrity == "conflicted" || source.Integrity == "quarantined" || source.Account != "" && source.Account != p.invocation.Account || source.Thread != "" && source.Thread != p.invocation.Thread || source.Sender != "" && source.Sender != p.p.trace.Sender) {
		return errors.New("project_plugin_source_changed")
	}
	if j.ID != "" {
		current, e := p.store.Job(ctx, j.ID)
		if e != nil || current.CancelRequested || current.Revision != j.Revision {
			return errors.New("project_plugin_job_changed")
		}
	}
	return nil
}

func (p *projectPluginPolicy) Service(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if method != "wechat/workspace_request" || p.requested || j.ID == "" {
		return nil, errors.New("project_plugin_service_denied")
	}
	if err := p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	var request struct {
		Tool      string          `json:"tool"`
		Operation string          `json:"operation"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if musicStrictJSON(raw, &request) != nil || request.Tool != p.request.Tool || request.Operation != p.tool.Operation || !sameJSON(request.Arguments, p.request.Arguments) {
		return nil, errors.New("project_plugin_request_changed")
	}
	p.requested = true // Reserve before any host I/O. Unknown effects are not replayed.
	longJob := j
	longJob.ID = "" // Long-running admission outlives the short broker Job CAS.
	authorized := func() bool {
		check, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return p.Check(check, longJob, cap) == nil
	}
	observation := p.mediate(ctx, authorized)
	if err := p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	p.observed, _ = json.Marshal(observation)
	return append(json.RawMessage(nil), p.observed...), nil
}

func (p *projectPluginPolicy) mediate(ctx context.Context, authorized func() bool) projectPluginObservation {
	var observation projectPluginObservation
	var value any
	var err error
	if p.tool.Operation == "snapshot" {
		return p.snapshot(ctx)
	}
	if p.tool.Operation == "mkdir" {
		value, err = p.p.mediateWorkspaceDirectory(ctx, p.request.Arguments)
	} else if p.tool.Operation == "process" || p.tool.Operation == "windows_process" {
		var result string
		result, err = p.p.mediateWorkspaceProcess(ctx, p.request.Arguments, p.tool.Operation == "windows_process", authorized)
		if err == nil {
			observation.Value = json.RawMessage(result)
		}
	} else {
		var cfg agenthost.Config
		cfg, err = p.p.scopedHostConfig(p.request.Arguments)
		if err == nil {
			call := func() (any, error) {
				return (agenthost.Host{Config: cfg}).Call(ctx, "server_"+p.tool.Operation, p.request.Arguments)
			}
			if p.p.privileged() && slices.Contains([]string{"exec", "write", "edit"}, p.tool.Operation) {
				var result string
				result, err = p.p.privilegedCall(ctx, p.request.Tool, p.request.Arguments, call)
				if err == nil {
					observation.Value = json.RawMessage(result)
				}
			} else {
				value, err = call()
			}
		}
	}
	if err != nil {
		var failure *RuntimeFailure
		if errors.As(err, &failure) {
			verified := *failure
			observation.Failure = &verified
		}
		observation.Error = localAgentError(err).Error()
	} else if len(observation.Value) == 0 {
		observation.Value, _ = json.Marshal(value)
	}
	return observation
}

// A pinned, non-following directory snapshot is a generic host read primitive.
// The package alone interprets JSON registrations and builds the project index.
func (p *projectPluginPolicy) snapshot(ctx context.Context) projectPluginObservation {
	x := p.p.executionContext()
	out := projectPluginObservation{Records: []projectPluginRecord{}, Execution: &x}
	if p.admin != nil {
		x = ExecutionContext{Account: p.admin.Account, Public: filepath.Join(p.p.manager.managedRoot(), "public_workspace"), Owner: true}
		out.Execution = &x
		if err := ensureManaged(x.Public); err != nil {
			out.Error = err.Error()
			return out
		}
	} else if _, err := p.p.hostConfig(); err != nil {
		out.Error = err.Error()
		return out
	}
	root, err := workspacefs.EnsurePath(x.Public)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer root.Close()
	dir, err := workspacefs.EnsureDirectory(root, "manifests")
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer dir.Close()
	var args struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(p.request.Arguments, &args)
	names := []string{}
	if p.request.Tool == "project_read" {
		if args.ID == "" || args.ID == "." || args.ID == ".." || filepath.Base(args.ID) != args.ID || strings.ContainsAny(args.ID, "/\\:\x00") {
			out.Error = "project_id_outside_registration"
			return out
		}
		names = append(names, args.ID+".json")
	} else {
		f, e := workspacefs.OpenDirectory(dir, ".")
		if e != nil {
			out.Error = e.Error()
			return out
		}
		entries, e := f.ReadDir(-1)
		f.Close()
		if e != nil {
			out.Error = e.Error()
			return out
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
				names = append(names, entry.Name())
			}
		}
		slices.Sort(names)
	}
	for _, name := range names {
		if err = ctx.Err(); err != nil {
			out.Error = err.Error()
			return out
		}
		record := projectPluginRecord{Name: name}
		f, e := workspacefs.OpenRegular(dir, name)
		if e != nil {
			if errors.Is(e, os.ErrNotExist) {
				record.Error = "project_not_registered"
			} else {
				record.Error = "project_manifest_unavailable"
			}
		} else {
			data, e := io.ReadAll(io.LimitReader(f, 65537))
			f.Close()
			if e != nil {
				record.Error = "project_manifest_unavailable"
			} else if !utf8.Valid(data) {
				record.Error = "project_manifest_invalid"
			} else if len(data) > 65536 {
				record.TooLarge = true
			} else {
				record.Content = string(data)
			}
		}
		out.Records = append(out.Records, record)
	}
	return out
}

func (p *projectPluginPolicy) Validate(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.Check(ctx, j, cap); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	if !p.requested {
		var rejected struct {
			Error string `json:"error"`
		}
		if musicStrictJSON(raw, &rejected) == nil && localMusicSafeCode(rejected.Error) {
			result, _ := json.Marshal(map[string]any{"error": rejected.Error, "not_started": true})
			return pluginruntime.VerifiedResult{Failed: true, Result: result}, nil
		}
	}
	var result projectPluginResult
	if musicStrictJSON(raw, &result) != nil || !p.requested || len(p.observed) == 0 {
		return pluginruntime.VerifiedResult{}, errors.New("project_plugin_result_unverified")
	}
	observed, _ := json.Marshal(result.Observation)
	if !sameJSON(observed, p.observed) {
		return pluginruntime.VerifiedResult{}, errors.New("project_plugin_observation_changed")
	}
	if p.tool.Operation != "snapshot" || result.Observation.Error != "" {
		if result.Error != result.Observation.Error || !sameNullableJSON(result.Value, result.Observation.Value) {
			return pluginruntime.VerifiedResult{}, errors.New("project_plugin_result_changed")
		}
	} else if !projectSnapshotProvenance(p.request.Tool, result) {
		return pluginruntime.VerifiedResult{}, errors.New("project_plugin_registration_unverified")
	}
	private, _ := json.Marshal(result)
	public, _ := json.Marshal(map[string]any{"operation": p.request.Tool, "host_observed": true, "wechat_sends": 0})
	return pluginruntime.VerifiedResult{Result: public, Private: private}, nil
}

func sameNullableJSON(a, b json.RawMessage) bool {
	if len(a) == 0 {
		a = json.RawMessage("null")
	}
	if len(b) == 0 {
		b = json.RawMessage("null")
	}
	return sameJSON(a, b)
}

// This verifies provenance of returned registrations, not their business schema.
// Every returned object must equal actual bytes from a single host-read record.
func projectSnapshotProvenance(tool string, result projectPluginResult) bool {
	objects := []json.RawMessage{}
	unavailable := map[string]projectPluginRecord{}
	for _, record := range result.Observation.Records {
		unavailable[record.Name] = record
	}
	for _, record := range result.Observation.Records {
		var object map[string]json.RawMessage
		if record.Error == "" && !record.TooLarge && json.Unmarshal([]byte(record.Content), &object) == nil && object != nil {
			objects = append(objects, json.RawMessage(record.Content))
			delete(unavailable, record.Name)
		}
	}
	if tool == "project_read" {
		if len(objects) == 0 {
			return result.Error != "" && sameNullableJSON(result.Value, nil)
		}
		return len(objects) == 1 && result.Error == "" && sameJSON(objects[0], result.Value)
	}
	var list struct {
		Projects  []json.RawMessage      `json:"projects"`
		Issues    []projectManifestIssue `json:"unavailable_manifests"`
		Execution ExecutionContext       `json:"execution"`
	}
	if musicStrictJSON(result.Value, &list) != nil || result.Error != "" || !reflect.DeepEqual(&list.Execution, result.Observation.Execution) || len(objects) != len(list.Projects) || len(objects)+len(list.Issues) != len(result.Observation.Records) {
		return false
	}
	for _, issue := range list.Issues {
		record, ok := unavailable[issue.Name]
		if !ok || issue.Reason == "" {
			return false
		}
		if record.Error != "" && issue.Reason != record.Error {
			return false
		}
		if record.Error == "" && record.TooLarge && issue.Reason != "project_manifest_metadata_too_large" {
			return false
		}
		if record.Error == "" && !record.TooLarge && issue.Reason != "project_manifest_invalid" {
			return false
		}
		delete(unavailable, issue.Name)
	}
	if len(unavailable) != 0 {
		return false
	}
	for i, object := range objects {
		if !sameJSON(object, list.Projects[i]) {
			return false
		}
	}
	return true
}

func (p *permittedTools) callProjectPlugin(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	policy, err := p.newProjectPluginPolicy(ctx, name, raw)
	if err != nil {
		return "", err
	}
	controller, ready, err := p.manager.installedPluginRuntime()
	if err != nil || !ready {
		return "", errors.New("project_plugin_runtime_unavailable")
	}
	if err = controller.EnsureStarted(ctx, projectPluginID, policy.installed.Revision); err != nil {
		return "", err
	}
	call, err := controller.Invoke(ctx, pluginruntime.CallRequest{Plugin: projectPluginID, Capability: projectPluginCapability(policy.tool.Permission), Context: policy.invocation, Arguments: policy.arguments}, policy)
	if err != nil {
		return "", err
	}
	if call.Job.State == "failed" {
		var rejected struct {
			Error      string `json:"error"`
			NotStarted bool   `json:"not_started"`
		}
		if musicStrictJSON(call.Job.Result, &rejected) == nil && rejected.NotStarted && localMusicSafeCode(rejected.Error) {
			return "", toolInputError(rejected.Error)
		}
	}
	if call.Job.State != "completed" {
		return "", errors.New("project_plugin_outcome_unknown_inspect_original_process_or_files")
	}
	private, err := policy.store.PrivateJobResult(ctx, call.Job, func(ctx context.Context, j pluginhost.Job) error {
		return policy.Check(ctx, j, pluginhost.Capability{})
	})
	if err != nil {
		return "", err
	}
	var result projectPluginResult
	if musicStrictJSON(private, &result) != nil {
		return "", errors.New("project_plugin_checkpoint_invalid")
	}
	if result.Error != "" {
		// Only the host-observed envelope enters the private checkpoint. Validate
		// compares it with the actual mediation record before it can be saved.
		if failure := result.Observation.Failure; failure != nil && failure.Message == result.Error && result.Error == result.Observation.Error {
			verified := *failure
			return "", &verified
		}
		return "", projectPluginResultError(result.Error)
	}
	return string(result.Value), nil
}

func projectPluginResultError(code string) error {
	switch code {
	case "project_not_registered":
		return toolInputError("project_not_registered: 未找到这个已登记项目。先调用 project_list 获取项目 ID；共享项目说明文件请使用 workspace_read 读取文件名，例如 project-yue.md。")
	case "project_manifest_unavailable":
		return toolInputError("project_manifest_unavailable: 项目登记文件当前不可读。可先调用 project_list 查看其他可用项目，或检查公共工作区的 manifests 目录。")
	default:
		return localAgentError(errors.New(code))
	}
}

var _ pluginruntime.ServicePolicy = (*projectPluginPolicy)(nil)
