package admin

// The installed package interprets synchronous external API responses. This
// adapter retains the original HTTP declaration, credentials and authority;
// the package can request one exact host-bound HTTP operation only.
import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/plugins"
	"wechat-bot/internal/workspacefs"
)

const externalHTTPPluginID = "external-tools"
const externalHTTPCapability = "external.http_request"
const externalJobCapability = "external.job_request"

type externalHTTPArguments struct {
	Phase           string             `json:"phase"`
	Tool            string             `json:"tool"`
	Arguments       json.RawMessage    `json:"arguments"`
	ArgumentsSHA256 string             `json:"arguments_sha256"`
	Protocol        json.RawMessage    `json:"protocol"`
	Ticket          *plugins.JobTicket `json:"ticket,omitempty"`
	TicketSHA256    string             `json:"ticket_sha256,omitempty"`
}
type externalHTTPResult struct {
	Observation plugins.ExternalResponse `json:"observation"`
	Output      plugins.Output           `json:"output"`
}

type externalHTTPBlobRef struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type externalHTTPObservation struct {
	ContentType string               `json:"content_type"`
	BodyBase64  string               `json:"body_base64,omitempty"`
	BinaryRef   *externalHTTPBlobRef `json:"binary_ref,omitempty"`
}
type externalHTTPWireMedia struct {
	plugins.MediaResult
	HostBinaryRef string `json:"host_binary_ref,omitempty"`
}
type externalHTTPWireOutput struct {
	Text  string                  `json:"text"`
	Media []externalHTTPWireMedia `json:"media,omitempty"`
}
type externalHTTPWireResult struct {
	Observation externalHTTPObservation `json:"observation"`
	Output      externalHTTPWireOutput  `json:"output"`
}
type externalHTTPCheckpoint struct {
	JobID       string              `json:"job_id,omitempty"`
	Binding     string              `json:"binding"`
	Body        externalHTTPBlobRef `json:"body"`
	Verified    externalHTTPBlobRef `json:"verified"`
	ContentType string              `json:"content_type"`
}

func (m *Manager) externalHTTPPackage(ctx context.Context) (*pluginhost.Store, pluginhost.InstalledPackage, int, error) {
	return m.externalPackageCapability(ctx, externalHTTPCapability)
}
func (m *Manager) externalPackageCapability(ctx context.Context, capability string) (*pluginhost.Store, pluginhost.InstalledPackage, int, error) {
	store, err := m.packageStore()
	if err != nil {
		return nil, pluginhost.InstalledPackage{}, 0, err
	}
	i, err := store.Installed(ctx, externalHTTPPluginID)
	if err != nil || !i.Enabled || i.Removed || i.Package.Status != "ready" || store.CheckConfiguration(ctx, externalHTTPPluginID) != nil {
		return nil, i, 0, errors.New("external_tools_plugin_required")
	}
	dir, err := store.PackageDirectory(i.Digest)
	if err != nil {
		return nil, i, 0, err
	}
	if _, err = pluginhost.Verify(dir); err != nil {
		return nil, i, 0, err
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil || namedSpeechSHA(manifest) != i.Digest {
		return nil, i, 0, errors.New("external_tools_package_changed")
	}
	caps, err := store.Capabilities(ctx, externalHTTPPluginID)
	if err != nil {
		return nil, i, 0, err
	}
	ready := false
	for _, cap := range caps {
		if cap.ID == capability && cap.Implementation == "ready" && cap.Discoverable && !cap.Async && slices.Equal(cap.Permissions, []string{"network"}) {
			ready = true
		}
	}
	if !ready || !slices.Contains(i.Permissions, "network") {
		return nil, i, 0, errors.New("external_tools_plugin_required")
	}
	config, err := store.Configuration(ctx, externalHTTPPluginID)
	if err != nil {
		return nil, i, 0, err
	}
	return store, i, config.Revision, nil
}

func (m *Manager) externalHTTPBridge() (*plugins.ExternalBridge, string, error) {
	items := m.plugins.installed()
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, "", err
	}
	// Detach every declaration/template/secret map before admitting an effect.
	var snapshot []plugins.Installed
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		return nil, "", err
	}
	bridge, err := plugins.NewExternalBridge(snapshot)
	return bridge, namedSpeechSHA(raw), err
}

func (p *permittedTools) externalLegacyHTTP(name string) bool {
	for _, item := range p.manager.plugins.installed() {
		if !item.Enabled {
			continue
		}
		for _, tool := range item.Manifest.Tools {
			if item.Manifest.ID+"__"+tool.Name == name {
				return tool.Job != nil || tool.Poll != nil
			}
		}
	}
	return false
}

func (p *permittedTools) installedExternalHTTPTools(ctx context.Context) []mcptools.Tool {
	if _, _, _, err := p.manager.externalHTTPPackage(ctx); err != nil {
		return nil
	}
	bridge, _, err := p.manager.externalHTTPBridge()
	if err != nil {
		return nil
	}
	tools := []mcptools.Tool{}
	for _, tool := range bridge.Protocol().Tools {
		if tool.Job == nil && tool.Poll == nil && p.apiNames[tool.Name] {
			tools = append(tools, mcptools.Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
		}
	}
	return tools
}

func (p *permittedTools) externalHTTPAuthorization(tool plugins.ExternalProtocolTool) string {
	return p.externalHTTPAuthorizationFor(tool, false)
}
func (p *permittedTools) externalHTTPAuthorizationFor(tool plugins.ExternalProtocolTool, allowInactiveTask bool) string {
	if p.privateSurfOrigin != nil || p.trace.Surf || !p.servicePluginsAllowed() || !allowInactiveTask && !p.manager.agentTaskActive(p.trace) {
		return ""
	}
	p.manager.mu.Lock()
	enabled := p.manager.mcp.Enabled
	p.manager.mu.Unlock()
	cfg := p.manager.agent.config()
	if !enabled || cfg.ExternalOwnerOnly && !p.privileged() || tool.MediaKind != "" && !companion.MediaPermitted(p.manager.Config().Companion.Permissions, tool.MediaKind) {
		return ""
	}
	raw, _ := json.Marshal([]any{p.trace, p.trace.TraceID, p.trace.QuoteServerID, p.privileged(), cfg, p.manager.Config().Companion.Permissions, enabled})
	return namedSpeechSHA(raw)
}

// A manual management test carries an authenticated session proof, never a
// fabricated WeChat sender or a permission to publish into any conversation.
type externalHTTPAdminContextKey struct{}
type externalHTTPAdminAuthority struct {
	Session *speechAdminTestAuthority
	Tool    string
}

func (s *Server) externalHTTPTestContext(ctx context.Context, r *http.Request, tool string) context.Context {
	// Reuse the established session validator, but expose only our distinct key
	// so this narrow permission cannot authorize another plugin's admin route.
	sessionContext := s.speechPluginTestContext(r)
	session, _ := sessionContext.Value(speechAdminTestContextKey{}).(*speechAdminTestAuthority)
	return context.WithValue(ctx, externalHTTPAdminContextKey{}, &externalHTTPAdminAuthority{Session: session, Tool: tool})
}
func (p *externalHTTPPolicy) adminAuthorization(ctx context.Context) (string, error) {
	if p.admin == nil || p.admin.Tool != p.request.Tool || p.request.Phase != "call" || p.tool.Job != nil || p.tool.Poll != nil {
		return "", errors.New("external_manual_scope_denied")
	}
	if err := p.admin.Session.valid(ctx, p.p.manager); err != nil {
		return "", err
	}
	p.p.manager.mu.Lock()
	enabled := p.p.manager.mcp.Enabled
	p.p.manager.mu.Unlock()
	permissions := p.p.manager.Config().Companion.Permissions
	if !enabled || p.tool.MediaKind != "" && !companion.MediaPermitted(permissions, p.tool.MediaKind) {
		return "", errors.New("external_manual_capability_disabled")
	}
	current, _ := json.Marshal([]any{enabled, p.p.manager.agent.config(), permissions})
	return agentID(p.admin.Session.Account, p.admin.Session.Session, p.admin.Session.RequestID, p.admin.Tool, p.request.ArgumentsSHA256, namedSpeechSHA(current)), nil
}

type externalHTTPPolicy struct {
	p              *permittedTools
	store          *pluginhost.Store
	installed      pluginhost.InstalledPackage
	configRevision int
	bridge         *plugins.ExternalBridge
	legacyHash     string
	tool           plugins.ExternalProtocolTool
	request        externalHTTPArguments
	arguments      json.RawMessage
	invocation     pluginhost.Invocation
	mu             sync.Mutex
	requested      bool
	observed       json.RawMessage
	body           externalHTTPBlobRef
	contentType    string
	execute        func(context.Context, plugins.ExternalRequest) (plugins.ExternalResponse, error)
	admin          *externalHTTPAdminAuthority
	job            *externalJobAuthority
	mcp            *externalMCPAuthority
}

func (p *permittedTools) newExternalHTTPPolicy(ctx context.Context, name string, raw json.RawMessage) (*externalHTTPPolicy, error) {
	return p.newExternalPolicy(ctx, name, raw, nil)
}
func (p *permittedTools) newExternalPolicy(ctx context.Context, name string, raw json.RawMessage, job *externalJobAuthority) (*externalHTTPPolicy, error) {
	trace, err := cloneProjectPluginTrace(p.trace)
	if err != nil {
		return nil, err
	}
	p = &permittedTools{manager: p.manager, trace: trace, privateDiagnostics: p.privateDiagnostics}
	capability := externalHTTPCapability
	if job != nil {
		capability = externalJobCapability
		if job.poll {
			capability = externalPollCapability
		}
	}
	store, installed, revision, err := p.manager.externalPackageCapability(ctx, capability)
	if err != nil {
		return nil, err
	}
	bridge, hash, err := p.manager.externalHTTPBridge()
	if err != nil {
		return nil, err
	}
	policy := &externalHTTPPolicy{p: p, store: store, installed: installed, configRevision: revision, bridge: bridge, legacyHash: hash, execute: bridge.Execute, job: job}
	policy.admin, _ = ctx.Value(externalHTTPAdminContextKey{}).(*externalHTTPAdminAuthority)
	protocol := bridge.Protocol()
	for _, tool := range protocol.Tools {
		if tool.Name == name {
			policy.tool = tool
			break
		}
	}
	var jobProtocol plugins.ExternalJobProtocol
	if job != nil {
		if job.poll {
			jobProtocol, err = bridge.PollProtocol(name)
		} else {
			jobProtocol, err = bridge.JobProtocol(name)
		}
		if err != nil {
			return nil, err
		}
		if jobProtocol.Tool.Fingerprint != job.task.PluginJob.Ticket.Fingerprint {
			return nil, plugins.ErrJobConfigChanged
		}
		if job.task.PluginJob.LegacyConfigurationSHA256 != "" && job.task.PluginJob.LegacyConfigurationSHA256 != hash {
			return nil, plugins.ErrJobConfigChanged
		}
		policy.tool = jobProtocol.Tool
		if job.poll {
			policy.execute = func(call context.Context, in plugins.ExternalRequest) (plugins.ExternalResponse, error) {
				return policy.executePoll(call, in)
			}
		}
	} else if policy.tool.Name == "" || policy.tool.Job != nil || policy.tool.Poll != nil {
		return nil, errors.New("external_sync_tool_unavailable")
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, toolInputError("external_arguments_invalid")
	}
	var canonical any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&canonical); err != nil {
		return nil, toolInputError("external_arguments_invalid")
	}
	raw, _ = json.Marshal(canonical)
	// A call needs only its selected declaration. Full discovery is separate;
	// unrelated schemas must not inflate every persisted request and stdio frame.
	protocol.Tools = []plugins.ExternalProtocolTool{policy.tool}
	protocolRaw, _ := json.Marshal(protocol)
	policy.request = externalHTTPArguments{Phase: "call", Tool: name, Arguments: raw, ArgumentsSHA256: namedSpeechSHA(raw), Protocol: protocolRaw}
	if job != nil {
		policy.request.Phase = job.phase
		ticket := job.task.PluginJob.Ticket
		policy.request.Ticket = &ticket
		encoded, _ := json.Marshal(ticket)
		policy.request.TicketSHA256 = namedSpeechSHA(encoded)
		policy.request.Protocol, _ = json.Marshal(jobProtocol)
	}
	policy.arguments, _ = json.Marshal(policy.request)
	authorization, err := policy.currentAuthorization(ctx)
	if err != nil {
		return nil, err
	}
	if authorization == "" {
		return nil, errors.New("external_http_authorization_denied")
	}
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
	key := "external-http-" + agentID(p.trace.Account, p.trace.Thread, p.trace.Sender, p.trace.TraceID, p.trace.TaskID, name, namedSpeechSHA(raw))
	policy.invocation = pluginhost.Invocation{Account: p.trace.Account, Thread: p.trace.Thread, Requester: p.trace.Sender, Purpose: "configured external HTTP tool " + name, RequestKey: key, Authorization: authorization, EvidenceEpoch: epoch, SourceVersions: sources}
	if policy.admin != nil {
		policy.invocation = pluginhost.Invocation{Account: policy.admin.Session.Account, Thread: "host:external-test", Requester: "administrator", Purpose: "manual registered external HTTP test", RequestKey: "external-http-test-" + agentID(authorization, name, policy.request.ArgumentsSHA256), Authorization: authorization}
	}
	if job != nil {
		policy.invocation.RequestKey = "external-job-" + agentID(job.task.Account, job.task.Thread, job.task.ID, job.phase, strconv.Itoa(job.task.PluginJob.Polls))
		policy.invocation.Purpose = "external job " + job.phase + " task " + job.task.ID
	}
	return policy, nil
}
func (p *externalHTTPPolicy) capability() string {
	if p.mcp != nil {
		return externalMCPCapability
	}
	if p.job != nil {
		if p.job.poll {
			return externalPollCapability
		}
		return externalJobCapability
	}
	return externalHTTPCapability
}
func (p *externalHTTPPolicy) currentAuthorization(ctx context.Context) (string, error) {
	if p.mcp != nil {
		return p.mcp.authorization(ctx, p)
	}
	if p.job != nil {
		return p.job.authorization(ctx, p)
	}
	if p.admin != nil {
		return p.adminAuthorization(ctx)
	}
	return p.p.externalHTTPAuthorization(p.tool), nil
}

func (p *externalHTTPPolicy) Check(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	authorization, err := p.currentAuthorization(ctx)
	if err != nil {
		return err
	}
	if j.Plugin != externalHTTPPluginID || j.Tool != p.capability() || cap.Async || cap.ID != "" && cap.ID != p.capability() || !reflect.DeepEqual(j.Context, p.invocation) || !sameJSON(j.Arguments, p.arguments) || p.invocation.Authorization == "" || authorization != p.invocation.Authorization {
		return errors.New("external_http_authorization_changed")
	}
	_, installed, revision, err := p.p.manager.externalPackageCapability(ctx, p.capability())
	if err != nil || installed.Digest != p.installed.Digest || installed.Revision != p.installed.Revision || !slices.Equal(installed.Permissions, p.installed.Permissions) || revision != p.configRevision || j.PackageDigest != "" && (j.PackageDigest != installed.Digest || j.Version != installed.Package.Version || j.ConfigRevision != strconv.Itoa(revision)) {
		return errors.New("external_http_package_or_configuration_changed")
	}
	var hash string
	if p.mcp != nil {
		hash, err = p.p.manager.externalMCPConfigurationHash()
	} else {
		_, hash, err = p.p.manager.externalHTTPBridge()
	}
	if err != nil || hash != p.legacyHash {
		return errors.New("external_http_declaration_changed")
	}
	if err = pluginhost.Authorize(installed.Package, pluginhost.Grant{Account: p.invocation.Account, Plugin: externalHTTPPluginID, Version: installed.Package.Version, Permissions: installed.Permissions}, j.Context, []string{"network"}); err != nil {
		return err
	}
	epoch, err := p.p.manager.memory.RecallEpoch(ctx)
	if p.admin != nil || p.mcp != nil && p.mcp.admin != nil {
		epoch = ""
	}
	if err != nil || epoch != p.invocation.EvidenceEpoch {
		return errors.New("external_http_evidence_changed")
	}
	for id, revision := range p.invocation.SourceVersions {
		ev, e := p.p.manager.memory.ReadEvent(ctx, []string{companion.ScopeID("wechat", p.invocation.Account, p.invocation.Thread)}, id)
		if e != nil || ev.Revision != revision {
			return errors.New("external_http_source_changed")
		}
		if source := p.p.trace.Source; source != nil && source.EventID == id && (ev.Role != "user" || ev.Sender != p.p.trace.Sender) {
			return errors.New("external_http_source_actor_changed")
		}
	}
	if source := p.p.trace.Source; source != nil && (source.Integrity == "conflicted" || source.Integrity == "quarantined" || source.Account != "" && source.Account != p.invocation.Account || source.Thread != "" && source.Thread != p.invocation.Thread || source.Sender != "" && source.Sender != p.p.trace.Sender) {
		return errors.New("external_http_source_changed")
	}
	if j.ID != "" {
		current, e := p.store.Job(ctx, j.ID)
		if e != nil || current.CancelRequested || current.Revision != j.Revision {
			return errors.New("external_http_job_changed")
		}
	}
	return nil
}

func (p *externalHTTPPolicy) Service(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	if p.mcp != nil {
		return p.mcpService(ctx, j, cap, method, raw)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if method != "wechat/external_request" || p.requested || j.ID == "" {
		return nil, errors.New("external_http_service_denied")
	}
	if err := p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	var request struct {
		Phase           string `json:"phase"`
		Tool            string `json:"tool"`
		ArgumentsSHA256 string `json:"arguments_sha256"`
		TicketSHA256    string `json:"ticket_sha256,omitempty"`
	}
	if musicStrictJSON(raw, &request) != nil || request.Phase != p.request.Phase || request.Tool != p.request.Tool || request.ArgumentsSHA256 != p.request.ArgumentsSHA256 || request.ArgumentsSHA256 != namedSpeechSHA(p.request.Arguments) || request.TicketSHA256 != p.request.TicketSHA256 {
		return nil, errors.New("external_http_request_changed")
	}
	p.requested = true // Unknown transport effects must never be resubmitted.
	bound, stop := p.boundContext(ctx, j, cap)
	defer stop()
	operation := plugins.ExternalRequest{Phase: p.request.Phase, Tool: p.request.Tool, Arguments: p.request.Arguments}
	if p.request.Ticket != nil {
		operation.Ticket = *p.request.Ticket
	}
	reply, err := p.execute(bound, operation)
	if err != nil {
		return nil, errors.New("external_http_outcome_unknown_do_not_resubmit")
	}
	if err = p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.Strict().DecodeString(reply.BodyBase64)
	if err != nil {
		return nil, errors.New("external_http_response_invalid")
	}
	if err = p.captureReply(j, data, reply.ContentType); err != nil {
		return nil, err
	}
	observation := externalHTTPObservation{ContentType: reply.ContentType, BodyBase64: reply.BodyBase64}
	if !externalHTTPIsJSON(data, reply.ContentType) {
		observation.BodyBase64 = ""
		ref := p.body
		observation.BinaryRef = &ref
	}
	p.observed, _ = json.Marshal(observation)
	return append(json.RawMessage(nil), p.observed...), nil
}

func (p *externalHTTPPolicy) boundContext(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability) (context.Context, context.CancelFunc) {
	bound, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-bound.Done():
				return
			case <-ticker.C:
				check, stop := context.WithTimeout(context.Background(), 2*time.Second)
				err := p.Check(check, j, cap)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return bound, func() { close(done); cancel() }
}
func (p *externalHTTPPolicy) captureReply(j pluginhost.Job, data []byte, contentType string) error {
	p.body = externalHTTPBlobRef{SHA256: namedSpeechSHA(data), Size: int64(len(data))}
	p.contentType = contentType
	directory := filepath.Join(p.replyRoot(), j.ID)
	if err := ensureManaged(p.replyRoot()); err != nil {
		return err
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return errors.New("external_http_reply_already_exists")
	}
	if err := writeExternalPrivate(filepath.Join(directory, "reply.bin"), data); err != nil {
		return err
	}
	return nil
}

func externalHTTPValue(data any, path string) any {
	if path == "" {
		return nil
	}
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			continue
		}
		object, ok := data.(map[string]any)
		if !ok {
			return nil
		}
		data = object[part]
	}
	return data
}
func externalHTTPText(value any) []string {
	if value == nil {
		return []string{""}
	}
	if text, ok := value.(string); ok {
		return []string{text}
	}
	encoded, _ := json.Marshal(value)
	return []string{string(encoded), fmt.Sprint(value)}
}

// Verify output provenance against the exact observed bytes and selected
// declaration. The package still owns extraction and protocol decisions.
func (p *externalHTTPPolicy) outputProof(reply plugins.ExternalResponse, output plugins.Output) bool {
	data, err := base64.StdEncoding.Strict().DecodeString(reply.BodyBase64)
	if err != nil {
		return false
	}
	var value any
	if !externalHTTPIsJSON(data, reply.ContentType) {
		if strings.Contains(strings.ToLower(reply.ContentType), "json") || output.Text != "" || len(output.Media) != 1 {
			return false
		}
		media := output.Media[0]
		binary, err := base64.StdEncoding.Strict().DecodeString(media.Base64Data)
		return err == nil && media.URL == "" && media.Kind == p.tool.MediaKind && media.Kind != "" && media.MIMEType == reply.ContentType && media.Filename == "" && bytes.Equal(binary, data)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return false
	}
	if !slices.Contains(externalHTTPText(externalHTTPValue(value, p.tool.TextPath)), output.Text) {
		return false
	}
	if len(output.Media) > 1 || p.tool.MediaURLPath != "" && len(output.Media) != 1 || p.tool.MediaURLPath == "" && len(output.Media) != 0 {
		return false
	}
	if len(output.Media) == 1 {
		media := output.Media[0]
		if media.Kind != p.tool.MediaKind || media.Kind == "" || media.Base64Data != "" || media.MIMEType != "" || media.Filename != "" || media.URL == "" {
			return false
		}
		expected, ok := externalHTTPValue(value, p.tool.MediaURLPath).(string)
		if !ok || expected != media.URL {
			return false
		}
	}
	return output.Text != "" || len(output.Media) > 0
}

func externalHTTPIsJSON(data []byte, contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "json") || json.Valid(data)
}
func (p *externalHTTPPolicy) replyRoot() string {
	return filepath.Join(p.p.manager.privateWorkspaceFor(p.invocation.Account), "external-http-replies")
}
func (p *externalHTTPPolicy) binding() string {
	raw, _ := json.Marshal([]any{p.invocation, p.arguments, p.installed.Digest, p.configRevision, p.legacyHash})
	return namedSpeechSHA(raw)
}
func writeExternalPrivate(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	closed := f.Close()
	if err != nil {
		return err
	}
	return closed
}
func (p *externalHTTPPolicy) readPrivateFile(j pluginhost.Job, name string, ref externalHTTPBlobRef, maximum int64) ([]byte, error) {
	if ref.Size < 0 || ref.Size > maximum || len(ref.SHA256) != 64 {
		return nil, errors.New("external_http_checkpoint_invalid")
	}
	root, err := workspacefs.EnsurePath(p.replyRoot())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := workspacefs.OpenRegular(root, filepath.Join(j.ID, name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, ref.Size+1))
	if err != nil || int64(len(data)) != ref.Size || namedSpeechSHA(data) != ref.SHA256 {
		return nil, errors.New("external_http_private_blob_changed")
	}
	return data, nil
}
func (p *externalHTTPPolicy) materialize(result externalHTTPWireResult, data []byte, body externalHTTPBlobRef, contentType string) (plugins.Output, error) {
	denied := errors.New("external_http_result_changed")
	if result.Observation.ContentType != contentType {
		return plugins.Output{}, denied
	}
	if !externalHTTPIsJSON(data, contentType) {
		if result.Observation.BinaryRef == nil || *result.Observation.BinaryRef != body || result.Observation.BodyBase64 != "" || result.Output.Text != "" || len(result.Output.Media) != 1 {
			return plugins.Output{}, denied
		}
		media := result.Output.Media[0]
		if media.HostBinaryRef != body.SHA256 || media.Kind != p.tool.MediaKind || media.Kind == "" || media.URL != "" || media.Base64Data != "" || media.MIMEType != "" || media.Filename != "" {
			return plugins.Output{}, denied
		}
		return plugins.Output{Media: []plugins.MediaResult{{Kind: media.Kind, Data: append([]byte(nil), data...), MIMEType: contentType}}}, nil
	}
	if result.Observation.BinaryRef != nil {
		return plugins.Output{}, denied
	}
	observed, err := base64.StdEncoding.Strict().DecodeString(result.Observation.BodyBase64)
	if err != nil || !bytes.Equal(data, observed) {
		return plugins.Output{}, denied
	}
	output := plugins.Output{Text: result.Output.Text}
	for _, media := range result.Output.Media {
		if media.HostBinaryRef != "" {
			return plugins.Output{}, denied
		}
		output.Media = append(output.Media, media.MediaResult)
	}
	if !p.outputProof(plugins.ExternalResponse{ContentType: contentType, BodyBase64: result.Observation.BodyBase64}, output) {
		return plugins.Output{}, denied
	}
	return output, nil
}
func (p *externalHTTPPolicy) Validate(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.Check(ctx, j, cap); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	if p.mcp != nil {
		return p.validateMCP(ctx, j, cap, raw)
	}
	if p.job != nil {
		if p.job.poll {
			return p.validatePoll(ctx, j, cap, raw)
		}
		return p.validateJob(ctx, j, cap, raw)
	}
	var result externalHTTPWireResult
	if musicStrictJSON(raw, &result) != nil || !p.requested || len(p.observed) == 0 {
		return pluginruntime.VerifiedResult{}, errors.New("external_http_result_unverified")
	}
	observation, _ := json.Marshal(result.Observation)
	if !sameJSON(observation, p.observed) {
		return pluginruntime.VerifiedResult{}, errors.New("external_http_result_changed")
	}
	data, err := p.readPrivateFile(j, "reply.bin", p.body, 25<<20)
	if err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	if _, err = p.materialize(result, data, p.body, p.contentType); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	return p.savePhaseResult(j, raw)
}
func (p *externalHTTPPolicy) savePhaseResult(j pluginhost.Job, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	if err := writeExternalPrivate(filepath.Join(p.replyRoot(), j.ID, "verified.json"), raw); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	checkpoint := externalHTTPCheckpoint{JobID: j.ID, Binding: p.binding(), Body: p.body, Verified: externalHTTPBlobRef{SHA256: namedSpeechSHA(raw), Size: int64(len(raw))}, ContentType: p.contentType}
	private, _ := json.Marshal(checkpoint)
	public, _ := json.Marshal(map[string]any{"tool": p.request.Tool, "phase": p.request.Phase, "host_observed": true, "wechat_sends": 0})
	if p.mcp != nil {
		public, _ = json.Marshal(map[string]any{"operation": p.mcp.request.Operation, "server_id": p.mcp.request.ServerID, "tool": p.mcp.request.Tool, "host_observed": true, "wechat_sends": 0})
	}
	return pluginruntime.VerifiedResult{Result: public, Private: private}, nil
}

func (p *permittedTools) callManualExternalHTTP(ctx context.Context, name string, args json.RawMessage) (string, error) {
	authority, _ := ctx.Value(externalHTTPAdminContextKey{}).(*externalHTTPAdminAuthority)
	if authority == nil || authority.Tool != name {
		return "", errors.New("external_manual_scope_denied")
	}
	if err := authority.Session.valid(ctx, p.manager); err != nil {
		return "", err
	}
	args, err := p.normalizeToolArgs(name, args)
	if err != nil {
		return "", err
	}
	output, err := p.callExternalHTTP(ctx, name, args)
	if err != nil {
		return "", err
	}
	ctx = withOutputAuthority(ctx, func(ctx context.Context) error { return authority.Session.valid(ctx, p.manager) })
	return p.stageOutput(ctx, output)
}

func (p *permittedTools) callExternalHTTP(ctx context.Context, name string, args json.RawMessage) (plugins.Output, error) {
	policy, err := p.newExternalHTTPPolicy(ctx, name, args)
	if err != nil {
		return plugins.Output{}, err
	}
	data, verified, checkpoint, err := policy.invokePhase(ctx)
	if err != nil {
		return plugins.Output{}, err
	}
	var result externalHTTPWireResult
	if musicStrictJSON(verified, &result) != nil {
		return plugins.Output{}, errors.New("external_http_checkpoint_invalid")
	}
	return policy.materialize(result, data, checkpoint.Body, checkpoint.ContentType)
}
func (policy *externalHTTPPolicy) invokePhase(ctx context.Context) ([]byte, []byte, externalHTTPCheckpoint, error) {
	c, ready, err := policy.p.manager.installedPluginRuntime()
	if err != nil || !ready {
		return nil, nil, externalHTTPCheckpoint{}, errors.New("external_tools_runtime_unavailable")
	}
	if err = c.EnsureStarted(ctx, externalHTTPPluginID, policy.installed.Revision); err != nil {
		return nil, nil, externalHTTPCheckpoint{}, err
	}
	call, err := c.Invoke(ctx, pluginruntime.CallRequest{Plugin: externalHTTPPluginID, Capability: policy.capability(), Context: policy.invocation, Arguments: policy.arguments}, policy)
	if err != nil {
		return nil, nil, externalHTTPCheckpoint{}, err
	}
	if call.Job.State != "completed" {
		return nil, nil, externalHTTPCheckpoint{}, errors.New("external_http_outcome_unknown_do_not_resubmit")
	}
	private, err := policy.store.PrivateJobResult(ctx, call.Job, func(ctx context.Context, j pluginhost.Job) error {
		return policy.Check(ctx, j, pluginhost.Capability{})
	})
	if err != nil {
		return nil, nil, externalHTTPCheckpoint{}, err
	}
	var checkpoint externalHTTPCheckpoint
	if musicStrictJSON(private, &checkpoint) != nil || checkpoint.Binding != policy.binding() {
		return nil, nil, externalHTTPCheckpoint{}, errors.New("external_http_checkpoint_invalid")
	}
	data, err := policy.readPrivateFile(call.Job, "reply.bin", checkpoint.Body, 25<<20)
	if err != nil {
		return nil, nil, externalHTTPCheckpoint{}, err
	}
	verified, err := policy.readPrivateFile(call.Job, "verified.json", checkpoint.Verified, 8<<20)
	if err != nil {
		return nil, nil, externalHTTPCheckpoint{}, err
	}
	if err = policy.Check(ctx, call.Job, pluginhost.Capability{}); err != nil {
		return nil, nil, externalHTTPCheckpoint{}, err
	}
	return data, verified, checkpoint, nil
}

var _ pluginruntime.ServicePolicy = (*externalHTTPPolicy)(nil)
