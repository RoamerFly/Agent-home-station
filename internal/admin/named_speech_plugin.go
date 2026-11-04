package admin

// Named speech is intentionally separate from cloud speechPluginPolicy: its
// identity is a registered local resource set, never a provider voice name.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func (m *Manager) namedSpeechPluginConfigured(ctx context.Context) bool {
	s, err := m.packageStore()
	if err != nil {
		return false
	}
	i, err := s.Installed(ctx, "speech")
	if err != nil || !i.Enabled || i.Removed || s.CheckConfiguration(ctx, "speech") != nil {
		return false
	}
	cfg, err := s.Configuration(ctx, "speech")
	if err != nil {
		return false
	}
	var profile string
	if json.Unmarshal(cfg.Values["local_profile_id"], &profile) != nil || profile != "gpt-sovits" {
		return false
	}
	inv := pluginhost.Invocation{Account: s.Account(), Thread: "host:named-speech", Requester: "host", Purpose: "check named speech", RequestKey: "named-speech-ready"}
	if pluginhost.Authorize(i.Package, pluginhost.Grant{Account: s.Account(), Plugin: i.Package.ID, Version: i.Package.Version, Permissions: i.Permissions}, inv, []string{"artifact.read", "model.invoke"}) != nil {
		return false
	}
	for _, cap := range mustCapabilities(ctx, s, "speech") {
		if cap.ID == "speech.synthesize_named" && cap.Discoverable && cap.Implementation == "ready" && !cap.Async {
			proof, ok, _ := namedSpeechManifestResourceProof(ctx, filepath.Join(m.managedRoot(), "public_workspace"))
			return ok && proof != ""
		}
	}
	return false
}

type namedSpeechRequest struct {
	VoiceID    string         `json:"voice_id"`
	Text       string         `json:"text"`
	Device     string         `json:"device"`
	Version    string         `json:"version"`
	Files      []string       `json:"file_ids"`
	Parameters map[string]any `json:"parameters"`
	Timeout    float64        `json:"timeout_seconds"`
}

// decodeNamedSpeechRequest deliberately validates the public tool contract
// before any voice catalogue, plugin, or inference work is touched.  Named
// GPT-SoVITS voices are not cloud TTS voices: in particular they do not have
// an "instruction" field.  Returning that distinction here lets the Agent
// correct an invalid call instead of repeatedly retrying the same request and
// reporting a misleading runtime failure.
func decodeNamedSpeechRequest(raw json.RawMessage) (namedSpeechRequest, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return namedSpeechRequest{}, toolInputError("named_speech_arguments_must_be_an_object")
	}
	allowed := map[string]bool{
		"voice_id": true, "text": true, "device": true, "version": true,
		"file_ids": true, "parameters": true, "timeout_seconds": true,
	}
	for key := range fields {
		if !allowed[key] {
			if key == "instruction" {
				return namedSpeechRequest{}, toolInputError("named_speech_unsupported_argument: instruction; named voices do not support instruction, use parameters.speed_factor (for example 0.8 for slower speech) when tuning is needed")
			}
			return namedSpeechRequest{}, toolInputError("named_speech_unsupported_argument: " + key + "; supported fields are text, voice_id, file_ids, parameters, device, version, timeout_seconds")
		}
	}
	var parameters map[string]any
	if value, ok := fields["parameters"]; ok && string(value) != "null" {
		if err := json.Unmarshal(value, &parameters); err != nil || parameters == nil {
			return namedSpeechRequest{}, toolInputError("named_speech_parameters_must_be_an_object")
		}
	}
	var in namedSpeechRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		return namedSpeechRequest{}, toolInputError("named_speech_argument_type_invalid")
	}
	if !namedSpeechVoiceIDPattern.MatchString(in.VoiceID) || in.VoiceID == "." || in.VoiceID == ".." {
		return namedSpeechRequest{}, toolInputError("named_speech_voice_id_required_or_invalid")
	}
	if strings.TrimSpace(in.Text) == "" || !utf8.ValidString(in.Text) || strings.ContainsRune(in.Text, 0) {
		return namedSpeechRequest{}, toolInputError("named_speech_text_required_or_invalid")
	}
	if value, ok := fields["timeout_seconds"]; ok {
		if json.Unmarshal(value, &in.Timeout) != nil || math.IsNaN(in.Timeout) || math.IsInf(in.Timeout, 0) || in.Timeout <= 0 || in.Timeout > float64((1<<63-1)/int64(1e9)) {
			return namedSpeechRequest{}, toolInputError("named_speech_timeout_seconds_must_be_positive")
		}
	} else {
		in.Timeout = 600
	}
	if in.Device == "" {
		in.Device = "cuda"
	}
	if !namedSpeechDevicePattern.MatchString(in.Device) {
		return namedSpeechRequest{}, toolInputError("named_speech_device_invalid_expected_cpu_or_cuda")
	}
	if in.Version == "" {
		in.Version = "v2"
	}
	if !namedSpeechVersionPattern.MatchString(in.Version) {
		return namedSpeechRequest{}, toolInputError("named_speech_version_invalid")
	}
	in.Parameters = parameters
	if in.Parameters == nil {
		in.Parameters = map[string]any{}
	}
	if err := validateNamedSpeechParameters(in.Parameters); err != nil {
		return namedSpeechRequest{}, toolInputError(err.Error())
	}
	return in, nil
}

var (
	namedSpeechVoiceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,128}$`)
	namedSpeechDevicePattern  = regexp.MustCompile(`^(cpu|cuda(:[0-9]+)?)$`)
	namedSpeechVersionPattern = regexp.MustCompile(`^(v1|v2|v2Pro|v2ProPlus|v3|v4|v5dev|v5turbo)$`)
)

type namedSpeechPluginPolicy struct {
	p                                            *permittedTools
	targetID                                     string // Only for an internal RVC reference recording.
	resource                                     namedSpeechResource
	request                                      namedSpeechRequest
	invocation                                   pluginhost.Invocation
	arguments                                    json.RawMessage
	store                                        *pluginhost.Store
	configRevision                               int
	packageDigest, packageVersion, manifestProof string
	grantProof                                   string
	mu                                           sync.Mutex
	requested                                    bool
	brokerReply                                  json.RawMessage
	checkpoint                                   namedSpeechCheckpoint
	completedJob                                 pluginhost.Job
	// Tests may substitute inference only; package/resource verification and
	// durable dispatch still run through the actual host policy.
	run func(context.Context, agenthost.Config, string, []string) ([]byte, []byte, error)
}

func (p *permittedTools) newNamedSpeechPluginPolicy(ctx context.Context, raw json.RawMessage) (*namedSpeechPluginPolicy, error) {
	return p.namedSpeechPolicy(ctx, raw, "")
}

func (p *permittedTools) namedSpeechPolicy(ctx context.Context, raw json.RawMessage, targetID string) (*namedSpeechPluginPolicy, error) {
	in, err := decodeNamedSpeechRequest(raw)
	if err != nil {
		return nil, err
	}
	if !p.workspaceExecutionAllowed() || p.manager.agent.config().Host.Mode != "full" || !p.manager.Config().Companion.Permissions.SendVoice {
		return nil, toolInputError("library_voice_requires_authorized_workspace_execution_and_voice_permission")
	}
	resource, err := p.namedSpeechResource(ctx, in.VoiceID, in.Files)
	if err != nil {
		return nil, err
	}
	if err := p.checkSpeechBinding(in.VoiceID, targetID); err != nil {
		return nil, err
	}
	store, err := p.manager.packageStore()
	if err != nil {
		return nil, err
	}
	installed, err := store.Installed(ctx, "speech")
	if err != nil || !installed.Enabled || installed.Removed {
		return nil, errors.New("named_speech_plugin_unavailable")
	}
	if store.CheckConfiguration(ctx, "speech") != nil {
		return nil, errors.New("named_speech_plugin_configuration_invalid")
	}
	cfg, err := store.Configuration(ctx, "speech")
	if err != nil {
		return nil, err
	}
	var profile string
	if json.Unmarshal(cfg.Values["local_profile_id"], &profile) != nil || profile != "gpt-sovits" {
		return nil, errors.New("named_speech_profile_not_configured")
	}
	ready := false
	for _, cap := range mustCapabilities(ctx, store, "speech") {
		if cap.ID == "speech.synthesize_named" && cap.Discoverable && cap.Implementation == "ready" && !cap.Async {
			ready = true
		}
	}
	if !ready {
		return nil, errors.New("named_speech_plugin_unavailable")
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
		for id, v := range p.trace.ReplyContext.SourceVersions {
			sources[id] = v
		}
	}
	auth := p.speechPluginAuthorization()
	if auth == "" {
		return nil, errors.New("named_speech_request_not_authorized")
	}
	args, _ := json.Marshal(map[string]any{"voice": map[string]string{"id": resource.Voice.ID, "version": resource.Version}, "text": in.Text, "device": in.Device, "version": in.Version, "parameters": in.Parameters, "timeout_seconds": in.Timeout})
	proof, ok, err := namedSpeechManifestResourceProof(ctx, p.executionContext().Public)
	if err != nil || !ok {
		return nil, errors.New("named_speech_registration_unavailable")
	}
	// The idempotency key names the original public request. Changing a resource,
	// package or evidence snapshot cannot manufacture a fresh inference retry.
	publicRequest, _ := json.Marshal(in)
	grants, _ := json.Marshal(installed.Permissions)
	inv := pluginhost.Invocation{Account: p.trace.Account, Thread: p.trace.Thread, Requester: p.serviceRequester(), Purpose: "synthesize named registered speech", RequestKey: "named-speech-" + agentID(p.trace.Account, p.trace.Thread, p.serviceRequester(), p.trace.TaskID, p.trace.TraceID, string(publicRequest)), EvidenceEpoch: epoch, Authorization: auth, SourceVersions: sources}
	if targetID != "" {
		inv.RequestKey = "named-speech-" + agentID(inv.RequestKey, "rvc-reference", targetID)
	}
	return &namedSpeechPluginPolicy{p: p, targetID: targetID, resource: resource, request: in, invocation: inv, arguments: args, store: store, configRevision: cfg.Revision, packageDigest: installed.Digest, packageVersion: installed.Package.Version, manifestProof: proof, grantProof: string(grants)}, nil
}

func (p *namedSpeechPluginPolicy) Check(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	want, _ := json.Marshal(p.invocation)
	got, _ := json.Marshal(j.Context)
	if p.p == nil || p.store == nil || j.Plugin != "speech" || j.Tool != "speech.synthesize_named" || cap.Async || !bytes.Equal(want, got) || p.invocation.Authorization == "" || p.invocation.Authorization != p.p.speechPluginAuthorization() || !p.p.workspaceExecutionAllowed() || p.p.manager.agent.config().Host.Mode != "full" {
		return errors.New("named_speech_authorization_changed")
	}
	if len(j.Arguments) > 0 {
		var requested, current any
		if json.Unmarshal(p.arguments, &requested) != nil || json.Unmarshal(j.Arguments, &current) != nil {
			return errors.New("named_speech_request_changed")
		}
		a, _ := json.Marshal(requested)
		b, _ := json.Marshal(current)
		if !bytes.Equal(a, b) {
			return errors.New("named_speech_request_changed")
		}
	}
	epoch, err := p.p.manager.memory.RecallEpoch(ctx)
	if err != nil || epoch != p.invocation.EvidenceEpoch {
		return errors.New("named_speech_evidence_changed")
	}
	for id, revision := range p.invocation.SourceVersions {
		event, err := p.p.manager.memory.ReadEvent(ctx, []string{companion.ScopeID("wechat", p.invocation.Account, p.invocation.Thread)}, id)
		if err != nil || event.Revision != revision {
			return errors.New("named_speech_source_changed")
		}
	}
	cfg, err := p.store.Configuration(ctx, "speech")
	if err != nil || cfg.Revision != p.configRevision {
		return errors.New("named_speech_configuration_changed")
	}
	i, err := p.store.Installed(ctx, "speech")
	if err != nil || !i.Enabled || i.Removed || i.Digest != p.packageDigest || i.Package.Version != p.packageVersion || (j.PackageDigest != "" && (j.PackageDigest != i.Digest || j.Version != i.Package.Version || j.ConfigRevision != strconv.Itoa(cfg.Revision))) {
		return errors.New("named_speech_package_changed")
	}
	grants, _ := json.Marshal(i.Permissions)
	if string(grants) != p.grantProof {
		return errors.New("named_speech_permissions_changed")
	}
	if pluginhost.Authorize(i.Package, pluginhost.Grant{Account: p.invocation.Account, Plugin: "speech", Version: i.Package.Version, Permissions: i.Permissions}, j.Context, []string{"artifact.read", "model.invoke"}) != nil {
		return errors.New("named_speech_permissions_changed")
	}
	dir, err := p.store.PackageDirectory(i.Digest)
	if err != nil {
		return err
	}
	if _, err = pluginhost.Verify(dir); err != nil {
		return errors.New("named_speech_package_integrity_changed")
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil || namedSpeechSHA(manifest) != p.packageDigest {
		return errors.New("named_speech_package_integrity_changed")
	}
	proof, ok, err := namedSpeechManifestResourceProof(ctx, p.p.executionContext().Public)
	if err != nil || !ok || proof != p.manifestProof {
		return errors.New("named_speech_registration_changed")
	}
	if err := p.p.checkSpeechBinding(p.request.VoiceID, p.targetID); err != nil {
		return err
	}
	r, err := p.p.namedSpeechResource(ctx, p.request.VoiceID, p.request.Files)
	if err != nil || r.Version != p.resource.Version {
		return errors.New("named_speech_resource_changed")
	}
	if j.ID != "" {
		current, err := p.store.Job(ctx, j.ID)
		if err != nil || current.CancelRequested || current.Revision != j.Revision {
			return errors.New("named_speech_task_changed")
		}
	}
	return nil
}

func (p *namedSpeechPluginPolicy) Service(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if method != "wechat/local_speech" || p.requested {
		return nil, errors.New("named_speech_host_service_unavailable")
	}
	if err := p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	var want, got map[string]any
	if strictServicePluginJSON(raw, &got) != nil || json.Unmarshal(p.arguments, &want) != nil {
		return nil, errors.New("named_speech_request_changed")
	}
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(got)
	if !bytes.Equal(a, b) {
		return nil, errors.New("named_speech_request_changed")
	}
	// Once accepted, an uncertain inference is never retried in this job.
	p.requested = true
	checkpoint, reply, err := p.execute(ctx, j, cap)
	if err != nil {
		return nil, err
	}
	if err = p.Check(ctx, j, cap); err != nil {
		return nil, err
	}
	p.checkpoint = checkpoint
	p.brokerReply = append(json.RawMessage(nil), reply...)
	return reply, nil
}

func (p *namedSpeechPluginPolicy) Validate(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.Check(ctx, j, cap); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	var reply namedSpeechBrokerResult
	if strictServicePluginJSON(raw, &reply) != nil || !p.requested || len(p.brokerReply) == 0 {
		return pluginruntime.VerifiedResult{}, errors.New("named_speech_result_unverified")
	}
	canonical, _ := json.Marshal(reply)
	if !bytes.Equal(canonical, p.brokerReply) {
		return pluginruntime.VerifiedResult{}, errors.New("named_speech_result_changed")
	}
	if p.checkpoint.JobID != j.ID {
		return pluginruntime.VerifiedResult{}, errors.New("named_speech_checkpoint_invalid")
	}
	if _, err := p.readCheckpoint(ctx, p.checkpoint); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	private, _ := json.Marshal(p.checkpoint)
	return pluginruntime.VerifiedResult{Result: canonical, Private: private}, nil
}

func (p *namedSpeechPluginPolicy) synthesize(ctx context.Context) (namedSpeechBrokerResult, error) {
	j := pluginhost.Job{Plugin: "speech", Tool: "speech.synthesize_named", Context: p.invocation}
	if err := p.Check(ctx, j, pluginhost.Capability{}); err != nil {
		return namedSpeechBrokerResult{}, err
	}
	c, ready, err := p.p.manager.installedPluginRuntime()
	if err != nil || !ready {
		return namedSpeechBrokerResult{}, errors.New("named_speech_runtime_unavailable")
	}
	i, err := p.store.Installed(ctx, "speech")
	if err != nil {
		return namedSpeechBrokerResult{}, err
	}
	if err = c.EnsureStarted(ctx, "speech", i.Revision); err != nil {
		return namedSpeechBrokerResult{}, err
	}
	call, err := c.Invoke(ctx, pluginruntime.CallRequest{Plugin: j.Plugin, Capability: j.Tool, Context: j.Context, Arguments: p.arguments}, p)
	if err != nil || call.Job.State != "completed" {
		return namedSpeechBrokerResult{}, errors.New("named_speech_synthesis_needs_review")
	}
	p.completedJob = call.Job
	raw, err := p.store.PrivateJobResult(ctx, call.Job, func(ctx context.Context, j pluginhost.Job) error { return p.Check(ctx, j, pluginhost.Capability{}) })
	if err != nil {
		return namedSpeechBrokerResult{}, errors.New("named_speech_result_unavailable")
	}
	var checkpoint namedSpeechCheckpoint
	if strictServicePluginJSON(raw, &checkpoint) != nil || checkpoint.Binding != p.binding() || checkpoint.JobID != call.Job.ID {
		return namedSpeechBrokerResult{}, errors.New("named_speech_checkpoint_invalid")
	}
	p.checkpoint = checkpoint
	if _, err = p.readAudio(ctx); err != nil {
		return namedSpeechBrokerResult{}, err
	}
	return checkpoint.Result, nil
}

func (p *namedSpeechPluginPolicy) binding() string {
	b, _ := json.Marshal([]any{p.invocation, p.arguments, p.resource.Version, p.manifestProof, p.packageDigest, p.configRevision, p.grantProof})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func (p *namedSpeechPluginPolicy) checkCompleted(ctx context.Context) error {
	if p.completedJob.ID == "" || p.completedJob.State != "completed" {
		return errors.New("named_speech_result_unavailable")
	}
	if err := p.Check(ctx, p.completedJob, pluginhost.Capability{}); err != nil {
		return err
	}
	if p.checkpoint.JobID != p.completedJob.ID {
		return errors.New("named_speech_checkpoint_invalid")
	}
	_, err := p.readCheckpoint(ctx, p.checkpoint)
	return err
}
func (p *namedSpeechPluginPolicy) readAudio(ctx context.Context) ([]byte, error) {
	if err := p.checkCompleted(ctx); err != nil {
		return nil, err
	}
	data, err := p.readCheckpoint(ctx, p.checkpoint)
	if err != nil {
		return nil, err
	}
	if err = p.checkCompleted(ctx); err != nil {
		return nil, err
	}
	return data, nil
}
