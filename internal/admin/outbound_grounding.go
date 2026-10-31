package admin

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"wechat-bot/internal/companion"
)

type unavailableOutboundReview struct{ err error }

// This error is minted only by deterministic admission checks, before a native
// submission. Transport errors and unknown delivery receipts never get this tag.
type outboundEvidenceError struct{ cause error }

func (e *outboundEvidenceError) Error() string { return e.cause.Error() }
func (e *outboundEvidenceError) Unwrap() error { return e.cause }
func (e *outboundEvidenceError) diagnostic() RuntimeFailure {
	code := e.cause.Error()
	for _, ch := range code {
		if ch != '_' && (ch < 'a' || ch > 'z') {
			code = "outbound_evidence_unavailable"
			break
		}
	}
	return RuntimeFailure{Code: code, Stage: "outbound_evidence", Message: "发送依据或授权未通过核验，未新增微信提交", NotStarted: true, Retryable: false, Continuation: "停止尚未发送部分，核对原始来源和当前权限；需要重新授权或重新生成计划。保留原幂等键查询已有回执，不换新键绕过核验。"}
}
func (e *outboundEvidenceError) SafeToolFailure() (string, string) {
	d := e.diagnostic()
	b, _ := json.Marshal(d)
	return d.Code, string(b)
}
func rejectOutboundEvidence(err error) error {
	if err == nil {
		return nil
	}
	var existing *outboundEvidenceError
	if errors.As(err, &existing) {
		return err
	}
	return &outboundEvidenceError{cause: err}
}

func (m unavailableOutboundReview) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", m.err
}

func (p *permittedTools) outboundInput(target string) companion.Input {
	in := p.trace
	in.ConversationDelivery = true
	in.IsGroup = strings.HasSuffix(target, "@chatroom") || in.IsGroup
	// Sources stay in their actual origin scope. Destination authorization is
	// independent; never fabricate a group source for a private owner request.
	in.SourceOnlyContext = target != in.Thread || in.Mode == "admin_action"
	if in.Source == nil || in.Source.EventID == "" || in.Source.Revision <= 0 {
		if source, ok := p.manager.memory.SourceForInput(in); ok {
			in.Source = source
		}
	}
	return in
}

func (p *permittedTools) validateOutboundText(ctx context.Context, target string, r companion.ReplyContext, texts ...string) error {
	allowed := p.conversationReadAllowed(target)
	// Ordinary members may explicitly request their own private reminder.
	// This grants no cross-conversation reading or arbitrary send tool access.
	if !allowed && target == p.trace.Sender && !strings.HasSuffix(target, "@chatroom") && p.trace.Account == p.manager.native.Account() {
		var err error
		allowed, err = p.manager.authorizeConversation(ctx, target)
		if err != nil {
			return err
		}
	}
	if !allowed || !p.manager.Config().Companion.Permissions.ReplyText {
		return rejectOutboundEvidence(errors.New("outbound_destination_authorization_revoked"))
	}
	in := p.outboundInput(target)
	if err := validateOutboundSource(ctx, p.manager, in, r, p); err != nil {
		return rejectOutboundEvidence(err)
	}
	e := &companion.Engine{Config: p.manager.Config().Companion, Store: p.manager.memory, Tools: p}
	for _, text := range texts {
		if err := e.ValidateReplyText(ctx, in, r, text); err != nil {
			return rejectOutboundEvidence(err)
		}
	}
	return nil
}

func validateOutboundSource(ctx context.Context, m *Manager, in companion.Input, r companion.ReplyContext, access *permittedTools) error {
	if in.Account != m.native.Account() || in.Thread == "" {
		return errors.New("outbound_source_identity_mismatch")
	}
	if r.EvidenceEpoch == "" {
		return errors.New("legacy_outbound_evidence_requires_review")
	}
	if access != nil && access.MemoryResultAuthorization() != "" && r.EvidenceAuthorization == "" {
		return errors.New("legacy_outbound_authorization_requires_review")
	}
	// Check source versions even for private targets, which otherwise need no
	// group semantic review. Forgotten sources never authorize new submissions.
	if in.Source != nil && in.Source.EventID != "" {
		ev, err := m.memory.ReadEvent(ctx, []string{companion.ScopeID(in.Channel, in.Account, in.Thread)}, in.Source.EventID)
		if err != nil || in.Source.Revision > 0 && ev.Revision != in.Source.Revision {
			return errors.New("outbound_source_unavailable_or_changed")
		}
	}
	e := &companion.Engine{Config: m.Config().Companion, Store: m.memory, Tools: access}
	return e.ValidateReplyContext(ctx, in, r)
}

// stampOutboundBinding is only used while preparing a new submission. Resume
// and retry paths validate their original stamps instead of renewing them.
func (p *permittedTools) stampOutboundBinding(ctx context.Context, in companion.Input, r companion.ReplyContext) (companion.ReplyContext, error) {
	epoch, err := p.manager.memory.RecallEpoch(ctx)
	if err != nil {
		return r, err
	}
	authorization := p.MemoryResultAuthorization()
	generation, hasGeneration := companion.GenerationEvidenceFromContext(ctx)
	if hasGeneration && (generation.RecallEpoch != epoch || generation.Authorization != authorization) {
		return r, errors.New("generation_evidence_changed")
	}
	if previous := in.ReplyContext; previous != nil {
		if previous.EvidenceEpoch == "" || authorization != "" && previous.EvidenceAuthorization == "" {
			return r, errors.New("legacy_outbound_evidence_requires_review")
		}
		if previous.EvidenceEpoch != epoch {
			return r, errors.New("reply_evidence_epoch_changed")
		}
		if previous.EvidenceAuthorization != "" && !p.MemoryResultAuthorized(previous.EvidenceAuthorization) {
			return r, errors.New("reply_evidence_authorization_changed")
		}
		// Keep the request's original evidence grant. A new destination cannot
		// grant current privileges to facts read by an earlier task generation.
		r.EvidenceEpoch = previous.EvidenceEpoch
		r.EvidenceAuthorization = previous.EvidenceAuthorization
	} else if hasGeneration {
		r.EvidenceEpoch = generation.RecallEpoch
		r.EvidenceAuthorization = generation.Authorization
	} else {
		r.EvidenceEpoch = epoch
		r.EvidenceAuthorization = authorization
	}
	return mintOutboundBinding(in, p.manager.Config().Companion, r), nil
}

func mintOutboundBinding(in companion.Input, c companion.Config, r companion.ReplyContext) companion.ReplyContext {
	r.State = "verified"
	if len(r.SourceEventIDs) == 0 {
		if in.Mode == "proactive" || in.Mode == "admin_action" || in.Surf || in.ReportOnly || in.TaskID != "" && in.BackgroundResult {
			r.State = "system_context"
		} else if !c.Permissions.Remember && in.Source != nil {
			r.State = "ephemeral_context"
		}
	}
	r.ContextVersion = companion.ReplyContextFingerprint(in, r)
	return r
}

func outboundAnchor(in companion.Input) string {
	if in.Source != nil && in.Source.MessageID != "" {
		return in.Source.MessageID
	}
	return in.TraceID
}

func (p *permittedTools) prepareOutboundText(ctx context.Context, target, text string) (string, companion.ReplyContext, error) {
	in := p.outboundInput(target)
	r := companion.ReplyContext{TargetIDs: []string{in.Sender}, AnchorMessageID: outboundAnchor(in), SourceVersions: map[string]int{}}
	if target != in.Thread {
		r.RiskFlags = []string{"cross_conversation_delivery"}
	}
	if in.Source != nil && in.Source.EventID != "" {
		r.SourceEventIDs = []string{in.Source.EventID}
		r.SourceVersions[in.Source.EventID] = in.Source.Revision
	}
	var err error
	r, err = p.stampOutboundBinding(ctx, in, r)
	if err != nil {
		return "", r, rejectOutboundEvidence(err)
	}
	if err := p.validateOutboundText(ctx, target, r, text); err != nil {
		return "", r, err
	}
	if !strings.HasSuffix(target, "@chatroom") {
		return text, r, nil
	}
	cfg := p.manager.runtimeConfig()
	reviewer := p.outboundReviewer
	if reviewer == nil {
		model, modelErr := p.manager.makeModel(cfg)
		reviewer = model
		if modelErr != nil {
			reviewer = unavailableOutboundReview{modelErr}
		}
	}
	// Low-risk sourced reminders need only deterministic validation. A missing
	// provider must fail when a risk actually needs review, not force an API
	// dependency onto harmless explicit reminders with no semantic model call.
	e, err := companion.New(cfg.sharedCompanion(), p.manager.memory, reviewer, cfg.SystemPrompt)
	if err != nil {
		return "", r, err
	}
	e.Tools = p
	d, err := e.ReviewGroundedReply(ctx, in, companion.Decision{Action: "reply", Messages: []string{text}, ReplyContexts: []companion.ReplyContext{r}})
	if err != nil {
		return "", r, err
	}
	if len(d.Messages) == 0 {
		return "", r, nil
	}
	// This API represents one native text submission. Keep each reviewed bubble
	// bound to the same authorized source rather than losing per-bubble targets.
	for i, candidate := range d.ReplyContexts {
		if err = e.ValidateReplyText(ctx, in, candidate, d.Messages[i]); err != nil {
			return "", r, err
		}
		if i > 0 && (!reflect.DeepEqual(candidate.TargetIDs, d.ReplyContexts[0].TargetIDs) || candidate.AnchorMessageID != d.ReplyContexts[0].AnchorMessageID) {
			return "", r, errors.New("outbound_multiple_targets_require_separate_messages")
		}
	}
	if len(d.ReplyContexts) > 0 {
		r = d.ReplyContexts[0]
	}
	return strings.Join(d.Messages, "\n"), r, nil
}

func reminderInput(t agentTask) companion.Input {
	in := companion.Input{Channel: "wechat", Account: t.Account, Thread: t.Thread, Sender: t.Sender, TraceID: t.OriginTrace, Text: t.RequestedText, IsGroup: strings.HasSuffix(t.Thread, "@chatroom")}
	in = *taskResultInput(t, &in)
	in.ConversationDelivery = true
	if in.Source != nil && in.Source.MessageID != "" {
		in.TraceID = in.Source.MessageID
	}
	return in
}
func validateReminderSource(ctx context.Context, m *Manager, t agentTask, texts ...string) error {
	if err := validateTaskConstraints(t); err != nil {
		return err
	}
	settings := m.agent.config()
	if !settings.Enabled || !settings.TasksEnabled || !m.Config().Companion.Permissions.ReplyText {
		return errors.New("reminder_authorization_revoked")
	}
	if t.Reminder == nil || t.Reminder.Target != t.Thread && t.Reminder.Target != t.Sender {
		return errors.New("reminder_target_not_requester")
	}
	if !t.Reminder.GroundingChecked {
		return errors.New("legacy_reminder_grounding_requires_review")
	}
	for _, thread := range []string{t.Thread, t.Reminder.Target} {
		ok, err := m.authorizeConversation(ctx, thread)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("reminder_conversation_revoked")
		}
	}
	in := reminderInput(t)
	if in.Source == nil {
		return errors.New("legacy_reminder_source_requires_verification")
	}
	r := companion.ReplyContext{TargetIDs: []string{t.Sender}, AnchorMessageID: t.OriginTrace}
	if t.Reminder.Binding != nil {
		r = *t.Reminder.Binding
	}
	// Restore the actual origin requester solely for live permission checks.
	// The persisted evidence stamp stays unchanged across restart/recovery.
	access := &permittedTools{manager: m, trace: in}
	if err := validateOutboundSource(ctx, m, in, r, access); err != nil {
		return err
	}
	e := &companion.Engine{Config: m.Config().Companion, Store: m.memory, Tools: access}
	if len(texts) == 0 {
		texts = []string{t.Reminder.Text}
	}
	for _, text := range texts {
		if err := e.ValidateReplyText(ctx, in, r, text); err != nil {
			return err
		}
	}
	return nil
}
