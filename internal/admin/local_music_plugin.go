package admin

// Local generation remains in the separately managed music service. The
// installed plugin controls a single host-bound request; it never receives a
// socket, credentials, local filenames or reference/audio bytes.
import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/workspacefs"
)

const localMusicCapability = "music.bridge_request"
const localMusicReplyLimit = 64 << 20

type localMusicBodyRef struct {
	Ref    string `json:"ref"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}
type localMusicRequest struct {
	Method string             `json:"method"`
	Path   string             `json:"path"`
	Body   *localMusicBodyRef `json:"body_ref,omitempty"`
	Key    string             `json:"idempotency_key,omitempty"`
}
type localMusicReply struct {
	Status int               `json:"status"`
	MIME   string            `json:"mime"`
	Body   localMusicBodyRef `json:"body_ref"`
	JSON   json.RawMessage   `json:"json,omitempty"`
}
type localMusicProviderError struct {
	Kind         string `json:"kind"`
	Code         string `json:"code"`
	ProviderCode string `json:"provider_code,omitempty"`
	Message      string `json:"message,omitempty"`
}
type localMusicPluginResult struct {
	localMusicReply
	Error *localMusicProviderError `json:"normalized_error,omitempty"`
}
type localMusicCheckpoint struct {
	JobID   string                   `json:"job_id"`
	Binding string                   `json:"binding"`
	Reply   localMusicReply          `json:"reply"`
	Error   *localMusicProviderError `json:"normalized_error,omitempty"`
}
type localMusicRecoveryKey struct{}
type localMusicRecoveryGrant struct {
	Task  agentTask
	JobID string
}

// Only the authenticated administrator recovery route mints this context. It
// permits inspection of one explicitly prepared job before its adoption task
// exists; it grants neither generation, audio access nor recovery mutations.
func withLocalMusicRecovery(ctx context.Context, task agentTask, jobID string) context.Context {
	return context.WithValue(ctx, localMusicRecoveryKey{}, localMusicRecoveryGrant{Task: copyTask(task), JobID: jobID})
}

func (g localMusicRecoveryGrant) proof(ctx context.Context, p *permittedTools, method, path string) (string, error) {
	t := g.Task
	if method != "GET" || !validID(g.JobID) || path != "/jobs/"+g.JobID || p.trace.TaskID != "" || t.Account != p.manager.native.Account() || t.Account != p.trace.Account || t.Thread != p.trace.Thread || t.Sender != p.trace.Sender || t.RecoveryMessageID == "" || t.OriginTrace != t.RecoveryMessageID || t.OriginTrace != p.trace.TraceID || t.MusicJob != "" && t.MusicJob != g.JobID {
		return "", errors.New("music_local_recovery_scope_denied")
	}
	msg, err := p.manager.native.Message(ctx, t.RecoveryMessageID)
	if err != nil || msg.ID != t.RecoveryMessageID || msg.Direction != "incoming" || msg.ThreadID != t.Thread || msg.SenderID != t.Sender || msg.Body != t.RequestedText {
		return "", errors.New("music_local_recovery_source_changed")
	}
	at, err := time.Parse(time.RFC3339Nano, msg.CreatedAt)
	if err != nil || time.Since(at) > 24*time.Hour || at.After(time.Now().Add(time.Minute)) || !strings.Contains(msg.Body, "歌") && !strings.Contains(msg.Body, "唱") {
		return "", errors.New("music_local_recovery_source_invalid")
	}
	b, _ := json.Marshal([]any{g.JobID, t.Account, t.Thread, t.Sender, t.RecoveryMessageID, msg.ID, msg.ServerID, msg.CreatedAt, msg.Body, t.Constraints})
	return namedSpeechSHA(b), nil
}

type localMusicPluginPolicy struct {
	p                                                   *permittedTools
	store                                               *pluginhost.Store
	invocation                                          pluginhost.Invocation
	request                                             localMusicRequest
	arguments                                           json.RawMessage
	body                                                []byte
	endpoint, token, digest, version, grants, taskProof string
	configRevision                                      int
	mu                                                  sync.Mutex
	requested                                           bool
	observed                                            json.RawMessage
	checkpoint                                          localMusicCheckpoint
	transportErr                                        error
	recovery                                            *localMusicRecoveryGrant
	admin                                               *speechAdminTestAuthority
	post                                                func(context.Context, string, string, []byte, string, string, string) ([]byte, string, int, error)
}

func localMusicEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || !musicProviderURL(raw) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.ContainsAny(raw, "?#") || strings.TrimRight(u.Path, "/") != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && (strings.EqualFold(u.Hostname(), "localhost") || ip != nil && (ip.IsLoopback() || ip.IsPrivate()))
}

// The route catalogue is deliberately closed. No query, URL, traversal or
// escaped separator can turn this private credential into a general proxy.
func localMusicRoute(method, path string) (string, bool) {
	if method == "GET" && (path == "/health" || path == "/rvc-health" || path == "/parameters" || path == "/sampling-options") {
		return "", true
	}
	if method == "POST" && (path == "/jobs" || path == "/rvc-jobs") {
		return "", true
	}
	parts := strings.Split(path, "/")
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "" || parts[1] != "jobs" || !validID(parts[2]) || strings.ContainsAny(parts[2], ".%?#\\") {
		return "", false
	}
	if len(parts) == 3 && method == "GET" || len(parts) == 4 && (method == "GET" && parts[3] == "audio" || method == "POST" && (parts[3] == "resume" || parts[3] == "cancel")) {
		return parts[2], true
	}
	return "", false
}

func (m *Manager) localMusicPluginConfigured(ctx context.Context) bool {
	s, err := m.packageStore()
	if err != nil {
		return false
	}
	i, err := s.Installed(ctx, "music")
	if err != nil || !i.Enabled || i.Removed {
		return false
	}
	c, err := s.ConfigurationForExecution(ctx, "music")
	if err != nil {
		return false
	}
	var endpoint, token string
	if json.Unmarshal(c.Values["local_endpoint"], &endpoint) != nil || json.Unmarshal(c.Values["local_token"], &token) != nil || !localMusicEndpoint(endpoint) || strings.TrimSpace(token) == "" {
		return false
	}
	inv := pluginhost.Invocation{Account: s.Account(), Thread: "host", Requester: "administrator", Purpose: "inspect local music readiness", RequestKey: "local-music-readiness"}
	if pluginhost.Authorize(i.Package, pluginhost.Grant{Account: s.Account(), Plugin: "music", Version: i.Package.Version, Permissions: i.Permissions}, inv, []string{"network"}) != nil {
		return false
	}
	caps, err := s.Capabilities(ctx, "music")
	if err != nil {
		return false
	}
	for _, cap := range caps {
		if cap.ID == localMusicCapability && cap.Implementation == "ready" && cap.Discoverable && !cap.Async {
			return true
		}
	}
	return false
}

// Lifecycle/health host adapters share the same installed configuration as
// inference. The retired speech settings never confer connection authority.
func (m *Manager) localMusicPluginConnection(ctx context.Context) (string, string, error) {
	if !m.localMusicPluginConfigured(ctx) {
		return "", "", toolInputError("music_local_plugin_configuration_required")
	}
	s, err := m.packageStore()
	if err != nil {
		return "", "", err
	}
	cfg, err := s.ConfigurationForExecution(ctx, "music")
	if err != nil {
		return "", "", err
	}
	var endpoint, token string
	if json.Unmarshal(cfg.Values["local_endpoint"], &endpoint) != nil || json.Unmarshal(cfg.Values["local_token"], &token) != nil || !localMusicEndpoint(endpoint) || strings.TrimSpace(token) == "" {
		return "", "", toolInputError("music_local_plugin_configuration_required")
	}
	return endpoint, token, nil
}

func (p *permittedTools) localMusicAuthorization() string {
	if p == nil || p.manager == nil || p.manager.native == nil || p.manager.agent == nil || p.trace.Preview || p.trace.ReportOnly || p.trace.Surf || p.trace.Account != p.manager.native.Account() || p.trace.Channel != "wechat" || p.trace.Thread == "" || p.trace.Sender == "" || !p.manager.agentTaskActive(p.trace) || !permittedRecipient(p.manager.Config().Config, p.trace.Thread) {
		return ""
	}
	// Existing persistent followups remain authorized while ingress is paused.
	// Their original task/source is checked separately on every use.
	b, _ := json.Marshal([]any{p.trace.Account, p.trace.Channel, p.trace.Thread, p.trace.Sender, p.trace.TaskID, p.trace.TraceID, p.trace.Source, p.trace.ReplyContext, p.trace.ExpectedVoiceID, p.trace.ExpectedVoiceProfile})
	return namedSpeechSHA(b)
}

func (p *permittedTools) localMusicTaskProof(method, path, id string) (string, error) {
	privilegeProof := ""
	if p.privileged() {
		settings := p.manager.agent.config()
		raw, _ := json.Marshal([]any{settings.Enabled, settings.OwnerIDs, settings.PrivilegedThreads, p.trace.Account, p.trace.Thread, p.trace.Sender})
		privilegeProof = namedSpeechSHA(raw)
	}
	proofFor := func(task agentTask) (string, error) {
		cancelledMetadata := task.State == "cancelled" && p.trace.TaskID == "" && method == "GET" && id != "" && path == "/jobs/"+id
		administrator := (method == "POST" || task.State == "cancelled") && task.Sender != p.trace.Sender
		if task.State == "cancelled" && !cancelledMetadata || p.manager.validateTaskSource(task) != nil || administrator && privilegeProof == "" {
			return "", errors.New("music_local_task_changed")
		}
		// Mutable polling/delivery fields are excluded. Administrator overrides
		// carry the live privilege proof, rechecked before and after transport.
		adminProof := ""
		if administrator {
			adminProof = privilegeProof
		}
		b, _ := json.Marshal([]any{task.ID, task.Account, task.Thread, task.Sender, task.OriginTrace, task.MusicJob, task.MusicSpec, task.Constraints, adminProof, cancelledMetadata})
		return namedSpeechSHA(b), nil
	}
	if p.trace.TaskID != "" {
		task, ok := p.manager.agent.lookupTask(p.trace.TaskID, p.trace.Account, p.trace.Thread)
		if !ok || id != "" && task.MusicJob != id || task.Sender != p.trace.Sender && (method != "POST" || privilegeProof == "") {
			return "", errors.New("music_local_task_changed")
		}
		return proofFor(task)
	}
	if id != "" {
		for _, task := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
			if task.MusicJob == id {
				return proofFor(task)
			}
		}
		// Legacy operation records prove conversation ownership for reads, but
		// contain no requester. Only an explicit current administrator may mutate
		// such a job before it has a requester-bound durable task.
		administrator := method == "POST"
		if administrator && privilegeProof == "" {
			return "", yueError("yue_job_scope_denied")
		}
		p.manager.agent.mu.Lock()
		defer p.manager.agent.mu.Unlock()
		for _, op := range p.manager.agent.data.Operations {
			if op.Thread != p.trace.Thread || op.Tool != "music_local_start" && op.Tool != "music_rvc_start" {
				continue
			}
			var result struct {
				ID string `json:"id"`
			}
			if json.Unmarshal([]byte(op.Result), &result) == nil && result.ID == id {
				adminProof := ""
				if administrator {
					adminProof = privilegeProof
				}
				b, _ := json.Marshal([]any{op, adminProof})
				return namedSpeechSHA(b), nil
			}
		}
		return "", yueError("yue_job_scope_denied")
	}
	return "", nil
}

func localMusicAdminAuthorization(ctx context.Context, m *Manager, authority *speechAdminTestAuthority, method, path string) (string, error) {
	if method != "GET" || path != "/health" && path != "/rvc-health" && path != "/parameters" {
		return "", errors.New("music_local_admin_route_denied")
	}
	if err := authority.valid(ctx, m); err != nil {
		return "", err
	}
	return agentID(authority.Account, authority.Session, authority.RequestID, method, path), nil
}

func (p *permittedTools) newLocalMusicPluginPolicy(ctx context.Context, method, path string, body any, key string) (*localMusicPluginPolicy, error) {
	id, ok := localMusicRoute(method, path)
	if !ok {
		return nil, errors.New("music_local_route_denied")
	}
	if method == "GET" && (body != nil || key != "") || method == "POST" && (path == "/jobs" || path == "/rvc-jobs" || strings.HasSuffix(path, "/resume")) && body == nil {
		return nil, errors.New("music_local_request_invalid")
	}
	authority, _ := ctx.Value(speechAdminTestContextKey{}).(*speechAdminTestAuthority)
	auth := p.localMusicAuthorization()
	if authority != nil {
		var err error
		auth, err = localMusicAdminAuthorization(ctx, p.manager, authority, method, path)
		if err != nil {
			return nil, err
		}
	} else if auth == "" {
		return nil, errors.New("music_local_request_not_authorized")
	}
	s, err := p.manager.packageStore()
	if err != nil {
		return nil, err
	}
	i, err := s.Installed(ctx, "music")
	if err != nil || !i.Enabled || i.Removed {
		return nil, toolInputError("music_local_plugin_migration_required")
	}
	if !p.manager.localMusicPluginConfigured(ctx) {
		return nil, toolInputError("music_local_plugin_configuration_required")
	}
	cfg, err := s.ConfigurationForExecution(ctx, "music")
	if err != nil {
		return nil, err
	}
	policy := &localMusicPluginPolicy{p: p, store: s, digest: i.Digest, version: i.Package.Version, configRevision: cfg.Revision, post: localMusicHTTP}
	policy.admin = authority
	_ = json.Unmarshal(cfg.Values["local_endpoint"], &policy.endpoint)
	_ = json.Unmarshal(cfg.Values["local_token"], &policy.token)
	grant, _ := json.Marshal(i.Permissions)
	policy.grants = string(grant)
	if authority != nil {
		policy.taskProof = ""
	} else if grant, ok := ctx.Value(localMusicRecoveryKey{}).(localMusicRecoveryGrant); ok {
		policy.recovery = &grant
		policy.taskProof, err = grant.proof(ctx, p, method, path)
	} else {
		policy.taskProof, err = p.localMusicTaskProof(method, path, id)
	}
	if err != nil {
		return nil, err
	}
	policy.request = localMusicRequest{Method: method, Path: path, Key: key}
	if body != nil {
		policy.body, err = json.Marshal(body)
		if err != nil {
			return nil, errors.New("music_local_request_invalid")
		}
		if len(policy.body) > 96<<20 {
			return nil, toolInputError("music_local_request_limit_96mib")
		}
		policy.request.Body = &localMusicBodyRef{Ref: "request_0", SHA256: namedSpeechSHA(policy.body), Bytes: int64(len(policy.body))}
	}
	if method != "GET" && key == "" {
		key = "music-local-" + agentID(p.trace.Account, p.trace.Thread, method, path, namedSpeechSHA(policy.body))
		policy.request.Key = key
	}
	if method == "GET" {
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		key = "music-observation-" + hex.EncodeToString(nonce[:])
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
		for id, rev := range p.trace.ReplyContext.SourceVersions {
			sources[id] = rev
		}
	}
	policy.invocation = pluginhost.Invocation{Account: p.trace.Account, Thread: p.trace.Thread, Requester: p.serviceRequester(), Purpose: "local music service request", RequestKey: key, Authorization: auth, EvidenceEpoch: epoch, SourceVersions: sources}
	if authority != nil {
		policy.invocation = pluginhost.Invocation{Account: authority.Account, Thread: "host:admin-music", Requester: "administrator", Purpose: "admin local music observation", RequestKey: key, Authorization: auth}
	}
	policy.arguments, _ = json.Marshal(policy.request)
	if err = policy.Check(ctx, pluginhost.Job{Plugin: "music", Tool: localMusicCapability, Context: policy.invocation}, pluginhost.Capability{}); err != nil {
		return nil, err
	}
	return policy, nil
}

func (p *localMusicPluginPolicy) Check(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a, _ := json.Marshal(p.invocation)
	b, _ := json.Marshal(j.Context)
	currentAuthorization := p.p.localMusicAuthorization()
	if p.admin != nil {
		var err error
		currentAuthorization, err = localMusicAdminAuthorization(ctx, p.p.manager, p.admin, p.request.Method, p.request.Path)
		if err != nil {
			return err
		}
	}
	if j.Plugin != "music" || j.Tool != localMusicCapability || cap.Async || cap.ID != "" && cap.ID != localMusicCapability || !bytes.Equal(a, b) || p.invocation.Authorization == "" || p.invocation.Authorization != currentAuthorization || len(j.Arguments) > 0 && !sameJSON(j.Arguments, p.arguments) {
		return errors.New("music_local_authorization_changed")
	}
	id, ok := localMusicRoute(p.request.Method, p.request.Path)
	if !ok {
		return errors.New("music_local_route_denied")
	}
	var proof string
	var err error
	if p.admin != nil {
		proof = ""
	} else if p.recovery != nil {
		proof, err = p.recovery.proof(ctx, p.p, p.request.Method, p.request.Path)
	} else {
		proof, err = p.p.localMusicTaskProof(p.request.Method, p.request.Path, id)
	}
	if err != nil || proof != p.taskProof {
		return errors.New("music_local_task_changed")
	}
	if p.admin == nil {
		epoch, err := p.p.manager.memory.RecallEpoch(ctx)
		if err != nil || epoch != p.invocation.EvidenceEpoch {
			return errors.New("music_local_evidence_changed")
		}
		for id, revision := range p.invocation.SourceVersions {
			ev, e := p.p.manager.memory.ReadEvent(ctx, []string{companion.ScopeID("wechat", p.invocation.Account, p.invocation.Thread)}, id)
			if e != nil || ev.Revision != revision {
				return errors.New("music_local_source_changed")
			}
			if source := p.p.trace.Source; source != nil && source.EventID == id && (ev.Role != "user" || ev.Sender != p.p.trace.Sender) {
				return errors.New("music_local_source_actor_changed")
			}
		}
		if source := p.p.trace.Source; source != nil && (source.Integrity == "conflicted" || source.Integrity == "quarantined" || source.Account != "" && source.Account != p.invocation.Account || source.Thread != "" && source.Thread != p.invocation.Thread || source.Sender != "" && source.Sender != p.p.trace.Sender) {
			return errors.New("music_local_source_changed")
		}
	}
	cfg, err := p.store.Configuration(ctx, "music")
	if err != nil || cfg.Revision != p.configRevision {
		return errors.New("music_local_configuration_changed")
	}
	i, err := p.store.Installed(ctx, "music")
	grants, _ := json.Marshal(i.Permissions)
	if err != nil || !i.Enabled || i.Removed || i.Digest != p.digest || i.Package.Version != p.version || string(grants) != p.grants || j.PackageDigest != "" && (j.PackageDigest != p.digest || j.Version != p.version || j.ConfigRevision != strconv.Itoa(p.configRevision)) {
		return errors.New("music_local_package_changed")
	}
	if pluginhost.Authorize(i.Package, pluginhost.Grant{Account: p.invocation.Account, Plugin: "music", Version: i.Package.Version, Permissions: i.Permissions}, j.Context, []string{"network"}) != nil {
		return errors.New("music_local_permissions_changed")
	}
	dir, err := p.store.PackageDirectory(i.Digest)
	if err != nil {
		return err
	}
	if _, err = pluginhost.Verify(dir); err != nil {
		return errors.New("music_local_package_integrity_changed")
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil || namedSpeechSHA(manifest) != p.digest {
		return errors.New("music_local_package_integrity_changed")
	}
	if j.ID != "" {
		current, e := p.store.Job(ctx, j.ID)
		if e != nil || current.CancelRequested || current.Revision != j.Revision {
			return errors.New("music_local_job_changed")
		}
	}
	return nil
}

func localMusicHTTP(ctx context.Context, method, endpoint string, body []byte, key, token, account string) ([]byte, string, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", 0, err
	}
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Wechat-Agent-Account", account)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	client := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", 0, yueError("yue_bridge_unavailable")
	}
	defer resp.Body.Close()
	if bound := resp.Header.Get("X-Wechat-Agent-Account"); bound != "" && bound != account {
		return nil, "", 0, yueError("yue_account_scope_mismatch")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, localMusicReplyLimit+1))
	if err != nil || len(data) > localMusicReplyLimit {
		return nil, "", 0, yueError("yue_response_limit")
	}
	return data, resp.Header.Get("Content-Type"), resp.StatusCode, nil
}

func (p *localMusicPluginPolicy) binding() string {
	b, _ := json.Marshal([]any{p.invocation, p.arguments, p.digest, p.version, p.configRevision, p.grants, p.taskProof})
	return namedSpeechSHA(b)
}
func (p *localMusicPluginPolicy) replyRoot() string {
	return filepath.Join(p.p.manager.privateWorkspaceFor(p.invocation.Account), "music-bridge-replies")
}

func (p *localMusicPluginPolicy) Service(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if method != "wechat/music_bridge" || p.requested || j.ID == "" {
		return nil, errors.New("music_local_host_service_unavailable")
	}
	if err := p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	var request localMusicRequest
	if musicStrictJSON(raw, &request) != nil || !sameJSON(raw, p.arguments) {
		return nil, errors.New("music_local_request_changed")
	}
	if request.Body != nil && (request.Body.Ref != "request_0" || request.Body.SHA256 != namedSpeechSHA(p.body) || request.Body.Bytes != int64(len(p.body))) {
		return nil, errors.New("music_local_request_changed")
	}
	// Reserve before transport. No callback/session/controller retry may issue
	// a second POST after an uncertain connection or response.
	p.requested = true
	data, mime, status, err := p.post(ctx, p.request.Method, strings.TrimRight(p.endpoint, "/")+p.request.Path, p.body, p.request.Key, p.token, accountDirectory(p.p.manager.native.Account()))
	if err != nil {
		p.transportErr = err
		return nil, err
	}
	if status < 100 || status > 599 || len(data) > localMusicReplyLimit || len(mime) > 512 {
		return nil, errors.New("music_local_response_invalid")
	}
	if err = p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	reply := localMusicReply{Status: status, MIME: mime, Body: localMusicBodyRef{Ref: "reply_0", SHA256: namedSpeechSHA(data), Bytes: int64(len(data))}}
	// Only the safe actual error field is needed by the package classifier.
	// Successful JSON, diagnostics, echoed credentials and audio stay private;
	// the body reference/hash continues to bind the full observed response.
	if (status < 200 || status > 299) && len(data) <= 256<<10 {
		var detail struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &detail) == nil && localMusicSafeCode(detail.Error) {
			reply.JSON, _ = json.Marshal(map[string]string{"error": detail.Error})
		}
	}
	root := p.replyRoot()
	if err = ensureManaged(root); err != nil {
		return nil, err
	}
	dir := filepath.Join(root, j.ID)
	if err = os.Mkdir(dir, 0700); err != nil {
		return nil, errors.New("music_local_reply_already_exists")
	}
	if err = os.WriteFile(filepath.Join(dir, "reply.bin"), data, 0600); err != nil {
		return nil, err
	}
	p.checkpoint = localMusicCheckpoint{JobID: j.ID, Binding: p.binding(), Reply: reply}
	p.observed, _ = json.Marshal(reply)
	if _, err = p.readCheckpoint(ctx, j, p.checkpoint); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), p.observed...), nil
}

func (p *localMusicPluginPolicy) readCheckpoint(ctx context.Context, j pluginhost.Job, c localMusicCheckpoint) ([]byte, error) {
	if err := p.Check(ctx, j, pluginhost.Capability{}); err != nil {
		return nil, err
	}
	if c.JobID != j.ID || c.Binding != p.binding() || c.Reply.Body.Ref != "reply_0" || c.Reply.Body.Bytes < 0 || c.Reply.Body.Bytes > localMusicReplyLimit {
		return nil, errors.New("music_local_checkpoint_invalid")
	}
	root, err := workspacefs.EnsurePath(p.replyRoot())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := workspacefs.OpenRegular(root, filepath.Join(j.ID, "reply.bin"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, localMusicReplyLimit+1))
	if err != nil || int64(len(data)) != c.Reply.Body.Bytes || namedSpeechSHA(data) != c.Reply.Body.SHA256 {
		return nil, errors.New("music_local_reply_changed")
	}
	if err = p.Check(ctx, j, pluginhost.Capability{}); err != nil {
		return nil, err
	}
	return data, nil
}

func (p *localMusicPluginPolicy) Validate(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.Check(ctx, j, cap); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	var result localMusicPluginResult
	if musicStrictJSON(raw, &result) != nil || !p.requested || len(p.observed) == 0 {
		return pluginruntime.VerifiedResult{}, errors.New("music_local_result_unverified")
	}
	reply := result.localMusicReply
	observed, _ := json.Marshal(reply)
	if !sameJSON(observed, p.observed) || !localMusicErrorProof(reply, result.Error) {
		return pluginruntime.VerifiedResult{}, errors.New("music_local_result_changed")
	}
	if err := p.verifyErrorTemplate(ctx, result.Error); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	if _, err := p.readCheckpoint(ctx, j, p.checkpoint); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	p.checkpoint.Error = result.Error
	private, _ := json.Marshal(p.checkpoint)
	// Even a provider error is a verified transport outcome. Preserve its
	// private bytes; provider classification belongs to the installed package.
	public, _ := json.Marshal(map[string]any{"status": reply.Status, "mime": reply.MIME, "body_ref": reply.Body, "wechat_sends": 0})
	return pluginruntime.VerifiedResult{Result: public, Private: private}, nil
}

func (p *localMusicPluginPolicy) invoke(ctx context.Context) ([]byte, string, int, error) {
	c, ready, err := p.p.manager.installedPluginRuntime()
	if err != nil || !ready {
		return nil, "", 0, errors.New("music_local_plugin_runtime_unavailable")
	}
	i, err := p.store.Installed(ctx, "music")
	if err != nil {
		return nil, "", 0, err
	}
	if c.Status("music").State == "stopped" {
		if err = c.Start(ctx, "music", i.Revision); err != nil {
			return nil, "", 0, errors.New("music_local_plugin_start_failed")
		}
	}
	call, err := c.Invoke(ctx, pluginruntime.CallRequest{Plugin: "music", Capability: localMusicCapability, Context: p.invocation, Arguments: p.arguments}, p)
	if err != nil {
		if p.transportErr != nil {
			return nil, "", 0, p.transportErr
		}
		return nil, "", 0, err
	}
	if call.Job.State != "completed" {
		return nil, "", 0, errors.New("music_local_plugin_request_needs_review")
	}
	private, err := p.store.PrivateJobResult(ctx, call.Job, func(ctx context.Context, j pluginhost.Job) error { return p.Check(ctx, j, pluginhost.Capability{}) })
	if err != nil {
		return nil, "", 0, err
	}
	var checkpoint localMusicCheckpoint
	if musicStrictJSON(private, &checkpoint) != nil {
		return nil, "", 0, errors.New("music_local_checkpoint_invalid")
	}
	data, err := p.readCheckpoint(ctx, call.Job, checkpoint)
	if err == nil && checkpoint.Error != nil {
		if !localMusicErrorProof(checkpoint.Reply, checkpoint.Error) {
			return nil, "", 0, errors.New("music_local_error_unverified")
		}
		if err = p.verifyErrorTemplate(ctx, checkpoint.Error); err != nil {
			return nil, "", 0, err
		}
		if checkpoint.Error.Kind == "service" {
			return nil, "", checkpoint.Reply.Status, yueError(checkpoint.Error.Code)
		}
		return nil, "", checkpoint.Reply.Status, toolInputError(checkpoint.Error.Message)
	}
	return data, checkpoint.Reply.MIME, checkpoint.Reply.Status, err
}

var localMusicSamplingCode = regexp.MustCompile(`^sampling_[a-z0-9_]+:[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

func localMusicSafeCode(s string) bool {
	if len(s) < 1 || len(s) > 160 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	if localMusicSamplingCode.MatchString(s) {
		return true
	}
	for _, c := range s {
		if c != '_' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// Verify the package's classification against transport observations. This is
// an integrity boundary, not a second provider parser or presentation engine.
func localMusicErrorProof(reply localMusicReply, normalized *localMusicProviderError) bool {
	if reply.Status >= 200 && reply.Status <= 299 {
		return normalized == nil
	}
	if normalized == nil || !localMusicSafeCode(normalized.Code) {
		return false
	}
	var detail struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(reply.JSON, &detail)
	provider := detail.Error
	if !localMusicSafeCode(provider) {
		provider = ""
	}
	if normalized.ProviderCode != provider {
		return false
	}
	if normalized.Kind == "service" {
		allowed := map[string][]int{"yue_account_scope_mismatch": {409}, "yue_bridge_auth_failed": {401, 403}, "yue_backend_unavailable": {503}, "yue_job_not_found": {404}, "yue_request_rejected": {}}
		statuses, ok := allowed[normalized.Code]
		if !ok {
			return false
		}
		if len(statuses) == 0 {
			return true
		}
		for _, status := range statuses {
			if status == reply.Status {
				return true
			}
		}
		return false
	}
	if normalized.Kind != "input" || normalized.Message == "" || len(normalized.Message) > 2048 || strings.ContainsAny(normalized.Message, "\x00\r\n") {
		return false
	}
	allowed := map[string]int{"rvc_recovery_failed": 409, "yue_recovery_restart_missing_required": 409, "yue_recovery_retry_failed_required": 409, "yue_recovery_failed": 409, "yue_native_parameters_invalid": 400, "rvc_service_not_ready": 503, "music_recovery_unavailable": 503}
	return allowed[normalized.Code] == reply.Status && provider != ""
}

func (p *localMusicPluginPolicy) verifyErrorTemplate(ctx context.Context, normalized *localMusicProviderError) error {
	if normalized == nil {
		return nil
	}
	i, err := p.store.Installed(ctx, "music")
	if err != nil || i.Digest != p.digest {
		return errors.New("music_local_error_template_unavailable")
	}
	const name = "runtime/error_templates.json"
	expected := i.Package.Files[name]
	if len(expected) != 64 {
		return errors.New("music_local_error_template_unavailable")
	}
	dir, err := p.store.PackageDirectory(p.digest)
	if err != nil {
		return err
	}
	root, err := workspacefs.EnsurePath(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := workspacefs.OpenRegular(root, filepath.FromSlash(name))
	if err != nil {
		return errors.New("music_local_error_template_unavailable")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(data) > 64<<10 || namedSpeechSHA(data) != expected {
		return errors.New("music_local_error_template_changed")
	}
	var document struct {
		Version   int `json:"version"`
		Templates map[string]struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		} `json:"templates"`
	}
	if musicStrictJSON(data, &document) != nil || document.Version != 1 {
		return errors.New("music_local_error_template_invalid")
	}
	template, ok := document.Templates[normalized.Code]
	if !ok || template.Kind != normalized.Kind || strings.ReplaceAll(template.Message, "{provider_code}", normalized.ProviderCode) != normalized.Message {
		return errors.New("music_local_error_template_mismatch")
	}
	return nil
}

var _ pluginruntime.ServicePolicy = (*localMusicPluginPolicy)(nil)
