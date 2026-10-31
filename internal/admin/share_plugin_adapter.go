package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"wechat-bot/internal/channels"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func (m *Manager) installedShareState(ctx context.Context) (bool, error) {
	return m.installedServiceState(ctx, "share-reader", "share")
}

func (m *Manager) installedServiceState(ctx context.Context, id, prefix string) (bool, error) {
	s, err := m.packageStore()
	if err != nil {
		return true, errors.New(prefix + "_plugin_store_unavailable")
	}
	i, err := s.Installed(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, errors.New(prefix + "_plugin_store_unavailable")
	}
	if !i.Enabled || i.Removed {
		return true, errors.New(prefix + "_plugin_disabled")
	}
	return true, nil
}

// This fingerprint contains original native identity/content, not observations
// or display names. Only its hash is kept in an ephemeral task's source metadata.
func shareNativeFingerprint(m linuxnative.Message) string {
	b, _ := json.Marshal([]any{m.AccountID, m.ThreadID, m.ID, m.SenderID, m.ServerID, m.CreatedAt, m.Direction, m.Type, m.AppMessageType, m.Body, m.Link})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type receivedSharePolicy struct {
	manager            *Manager
	store              *pluginhost.Store
	message            linuxnative.Message
	source             companion.SourceRef
	fingerprint, epoch string
	remember           bool
	tool               string
	requestSuffix      string
	binding            *shareRouteBinding
	send               pluginruntime.RouteTransport
}

func (m *Manager) newReceivedSharePolicy(ctx context.Context, message linuxnative.Message) (*receivedSharePolicy, error) {
	return m.newReceivedSharePolicyFor(ctx, message, "share.resolve_channels")
}

func (m *Manager) newReceivedSharePolicyFor(ctx context.Context, message linuxnative.Message, tool string) (*receivedSharePolicy, error) {
	validKind := message.AppMessageType != nil && (*message.AppMessageType == 51 && tool == "share.resolve_channels" || (*message.AppMessageType == 4 || *message.AppMessageType == 5) && tool == "share.resolve_public" && message.Link != nil && sharePublicURL(message.Link.URL) != "")
	validKind = message.Type == 49 && validKind || tool == "media.observe" && (message.Type == 3 || message.Type == 47 || message.Type == 43 || message.Type == 49 && message.AppMessageType != nil && (*message.AppMessageType == 4 || *message.AppMessageType == 5 || *message.AppMessageType == 51)) || tool == "media.transcribe" && message.Type == 34
	if m.memory == nil || message.ID == "" || message.ThreadID == "" || message.SenderID == "" || message.Direction != "incoming" || !validKind {
		return nil, errors.New("share_native_source_required")
	}
	if message.AccountID == "" {
		message.AccountID = m.native.Account()
	}
	if message.AccountID != m.native.Account() {
		return nil, errors.New("share_source_scope_mismatch")
	}
	store, err := m.packageStore()
	if err != nil {
		return nil, err
	}
	epoch, err := m.memory.RecallEpoch(ctx)
	if err != nil {
		return nil, err
	}
	p := &receivedSharePolicy{manager: m, store: store, message: message, tool: tool, fingerprint: receivedSourceFingerprint(message, tool), epoch: epoch, remember: m.Config().Companion.Permissions.Remember, send: mcptools.PostPluginRoute}
	if p.remember {
		ref, ok := companion.MediaSourceFromContext(ctx)
		if !ok {
			// Explicit rereads can resolve an existing source, never create one
			// from model arguments or infer identity from a neighboring message.
			original := ""
			in := companion.Input{Channel: "wechat", Account: message.AccountID, Thread: message.ThreadID, Sender: message.SenderID, TraceID: message.ID, MessageTime: message.CreatedAt, OriginalText: &original, Source: &companion.SourceRef{InstanceID: m.memory.SourceInstanceID(), MessageID: message.ID, ServerID: message.ServerID}}
			found, valid := m.memory.SourceForInput(in)
			if !valid {
				return nil, errors.New("share_persisted_source_unavailable")
			}
			ref = *found
		}
		if ref.Account != message.AccountID || ref.Thread != message.ThreadID || ref.MessageID != message.ID || ref.Sender != message.SenderID {
			return nil, errors.New("share_source_scope_mismatch")
		}
		p.source = ref
	} else {
		p.source = companion.SourceRef{Account: message.AccountID, Thread: message.ThreadID, Sender: message.SenderID, MessageID: message.ID, ServerID: message.ServerID, EventID: "native:" + p.fingerprint, Revision: 1, MessageType: message.Type, OccurredAt: message.CreatedAt}
	}
	return p, nil
}

func (p *receivedSharePolicy) invocation() pluginhost.Invocation {
	c := pluginhost.Invocation{Account: p.message.AccountID, Thread: p.message.ThreadID, Requester: p.message.SenderID, Purpose: "read received video share", RequestKey: "received-share-" + agentID(p.source.EventID, p.message.ID), SourceVersions: map[string]int{p.source.EventID: p.source.Revision}, EvidenceEpoch: p.epoch, Authorization: p.fingerprint}
	if p.tool == "media.observe" || p.tool == "media.transcribe" {
		c.Purpose = "observe received media"
		c.RequestKey = "received-media-" + agentID(p.source.EventID, p.message.ID, p.fingerprint, p.requestSuffix)
	}
	return c
}

func receivedSourceFingerprint(m linuxnative.Message, tool string) string {
	if tool != "media.observe" && tool != "media.transcribe" {
		return shareNativeFingerprint(m)
	}
	b, _ := json.Marshal([]any{shareNativeFingerprint(m), m.Attachments})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (p *receivedSharePolicy) Check(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c := p.manager.Config().Config
	plugin, readAllowed := "share-reader", c.Behavior.ReadVideos
	if p.tool == "media.observe" || p.tool == "media.transcribe" {
		plugin = "media-understanding"
		if p.tool == "media.transcribe" {
			readAllowed = p.manager.speechConfig().ASREnabled
		} else if p.message.Type == 3 || p.message.Type == 47 {
			readAllowed = c.Behavior.ReadImages
		}
	}
	if j.Plugin != plugin || j.Tool != p.tool || cap.Async || !c.Enabled || !readAllowed || !permittedRecipient(c, p.message.ThreadID) || c.Companion.Permissions.Remember != p.remember || p.manager.native.Account() != p.message.AccountID {
		return errors.New("share_request_authorization_changed")
	}
	if strings.HasSuffix(p.message.ThreadID, "@chatroom") && !c.Companion.Permissions.ObserveGroups {
		return errors.New("share_request_authorization_changed")
	}
	i, err := p.store.Installed(ctx, j.Plugin)
	if err != nil || !i.Enabled || i.Removed {
		return errors.New("share_plugin_disabled")
	}
	expected := p.invocation()
	a, _ := json.Marshal(j.Context)
	b, _ := json.Marshal(expected)
	if string(a) != string(b) {
		return errors.New("share_invocation_changed")
	}
	epoch, err := p.manager.memory.RecallEpoch(ctx)
	if err != nil || epoch != p.epoch {
		return errors.New("share_source_changed")
	}
	if p.remember {
		ev, err := p.manager.memory.ReadEvent(ctx, []string{companion.ScopeID("wechat", p.message.AccountID, p.message.ThreadID)}, p.source.EventID)
		if err != nil || ev.Revision != p.source.Revision || ev.Sender != p.message.SenderID || ev.Trace != p.message.ID || ev.Hash != p.source.BodyHash {
			return errors.New("share_source_changed")
		}
	}
	current, err := p.manager.native.Message(ctx, p.message.ID)
	if err != nil {
		return errors.New("share_native_source_unavailable")
	}
	if current.AccountID == "" {
		current.AccountID = p.manager.native.Account()
	}
	if receivedSourceFingerprint(current, p.tool) != p.fingerprint {
		return errors.New("share_native_source_changed")
	}
	return nil
}

func (p *receivedSharePolicy) Service(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	if method != "wechat/http_route" || p.binding == nil {
		return nil, errors.New("share_host_service_unavailable")
	}
	return p.binding.Call(ctx, p.store, j, cap, raw, func(ctx context.Context) error { return p.Check(ctx, j, cap) }, p.send)
}

func (p *receivedSharePolicy) Validate(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	if p.binding == nil {
		return pluginruntime.VerifiedResult{}, errors.New("share_source_binding_unavailable")
	}
	verified, _, err := p.binding.ValidateResult(ctx, j, raw, func(ctx context.Context) error { return p.Check(ctx, j, cap) })
	return verified, err
}

func (m *Manager) resolveInstalledChannels(ctx context.Context, message linuxnative.Message, source linuxnative.ChannelsSource) (linuxnative.ChannelsSource, error) {
	p, err := m.newReceivedSharePolicy(ctx, message)
	if err != nil {
		return source, err
	}
	return p.resolve(ctx, source)
}

func (p *receivedSharePolicy) resolve(ctx context.Context, source linuxnative.ChannelsSource) (linuxnative.ChannelsSource, error) {
	m, message := p.manager, p.message
	var err error
	job := pluginhost.Job{Plugin: "share-reader", Tool: "share.resolve_channels", Context: p.invocation()}
	if err = p.Check(ctx, job, pluginhost.Capability{}); err != nil {
		return source, err
	}
	args := map[string]any{}
	link := ""
	if strings.HasPrefix(source.ObjectID, "export/") {
		args["export_id"], args["nonce_id"] = source.ObjectID, source.ObjectNonceID
	} else {
		native, ok := m.native.(interface {
			ChannelsShareURL(context.Context, string, string, string) (string, error)
		})
		if !ok || source.ObjectID == "" {
			return source, errors.New("share_native_shortlink_required")
		}
		link, err = native.ChannelsShareURL(ctx, message.ID, message.ThreadID, source.ObjectID)
		if err != nil {
			return source, errors.New("share_native_shortlink_unavailable")
		}
		link, err = channels.ShareURL(link)
		if err != nil {
			return source, errors.New("share_native_shortlink_invalid")
		}
		args["url"], args["expected_object_id"] = link, source.ObjectID
	}
	p.binding, err = newShareRouteBinding(p.message, source, p.source, link)
	if err != nil {
		return source, err
	}
	h, ready, err := m.installedPluginRuntime()
	if err != nil || !ready {
		return source, errors.New("share_plugin_runtime_unavailable")
	}
	i, err := p.store.Installed(ctx, "share-reader")
	if err != nil {
		return source, err
	}
	if h.Status("share-reader").State == "stopped" {
		if err = h.Start(ctx, "share-reader", i.Revision); err != nil {
			return source, errors.New("share_plugin_start_failed")
		}
	}
	raw, _ := json.Marshal(args)
	out, err := h.Invoke(ctx, pluginruntime.CallRequest{Plugin: job.Plugin, Capability: job.Tool, Context: job.Context, Arguments: raw}, p)
	if err != nil {
		return source, errors.New("share_plugin_resolution_incomplete")
	}
	if out.Job.State != "completed" {
		return source, errors.New("share_plugin_resolution_needs_review")
	}
	private, err := p.store.PrivateJobResult(ctx, out.Job, func(ctx context.Context, j pluginhost.Job) error { return p.Check(ctx, j, pluginhost.Capability{}) })
	if err != nil {
		return source, errors.New("share_plugin_handoff_unavailable")
	}
	var resolved linuxnative.ChannelsSource
	if json.Unmarshal(private, &resolved) != nil || resolved.AccountID != p.message.AccountID || resolved.ThreadID != message.ThreadID || resolved.MessageID != message.ID || resolved.MediaURL == "" {
		return source, errors.New("share_plugin_handoff_invalid")
	}
	return resolved, nil
}
