package bot

import (
	"context"
	"errors"
	"strings"
	"wechat-bot/internal/companion"
)

type replyGroundingEngine interface {
	ValidateReplyContext(context.Context, companion.Input, companion.ReplyContext) error
	ReviewGroundedReply(context.Context, companion.Input, companion.Decision) (companion.Decision, error)
}

var errReplyEvidenceEpochChanged = errors.New("reply_evidence_epoch_changed")

// Evidence helpers only read the source and engine. Bind both before checking
// a queued reply; the generation's per-conversation adapter has already exited.
func (w *Worker) deliveryEvidenceReader(in companion.Input) (*Worker, error) {
	engine, ok := w.model.(*companion.Engine)
	if !ok {
		return w, nil
	}
	scoped, err := engine.ForEvidenceInput(in)
	if err != nil {
		return nil, err
	}
	return &Worker{model: scoped, source: w.source}, nil
}

func (w *Worker) prepareGroundedText(ctx context.Context, in companion.Input, text string) (companion.Input, string, error) {
	ctx = w.usageContext(ctx, in.Thread, in.TraceID)
	if w.config.AuthorizeReply != nil && !w.config.AuthorizeReply(in) {
		return in, text, errors.New("conversation_grant_changed")
	}
	in.IsGroup = in.IsGroup || strings.HasSuffix(in.Thread, "@chatroom")
	w, scopeErr := w.deliveryEvidenceReader(in)
	if scopeErr != nil {
		return in, text, scopeErr
	}
	engine, ok := w.model.(replyGroundingEngine)
	if !ok {
		// Non-character adapters have no semantic source store. Preserve their
		// existing interface; production companion replies always use it.
		return in, text, nil
	}
	// A new, unrelated memory entry can advance the global recall epoch while
	// this already-authored bubble is waiting for its send slot. Keep its
	// original wording when every source bound to this bubble is unchanged.
	// The normal reply validator below still checks the original source,
	// destination, authorization and text before submission.
	var err error
	in, err = w.refreshUnchangedReplyEvidence(ctx, in)
	if err != nil {
		return in, text, err
	}
	if err := w.validateExistingReplyEvidence(ctx, in); err != nil {
		return in, text, err
	}
	if !in.IsGroup && (in.ReplyContext == nil || w.bindingEvidenceMissing(in) || replyProofMetadataMissing(in.ReplyContext)) {
		// A legacy private draft may depend on forgotten secondary material.
		// Its surviving trigger proves neither those facts nor their currency.
		// Only a fresh model call's still-valid host stamp may authorize a new
		// structural binding; old queued text requires an explicit replan.
		e, ok := w.model.(*companion.Engine)
		evidence, fresh := companion.GenerationEvidenceFromContext(ctx)
		if !ok || !fresh {
			return in, text, errors.New("private_reply_requires_replan")
		}
		if err := e.ValidateGenerationEvidence(ctx, evidence); err != nil {
			return in, text, err
		}
	}
	ephemeral := false
	legacyRecovered := in.Source == nil
	if e, ok := w.model.(*companion.Engine); ok && !e.Config.Permissions.Remember && in.Sender != "" && in.Mode != "proactive" && !hostActionProof(in) {
		var err error
		in, err = w.verifyEphemeralSource(ctx, in)
		if err != nil {
			return in, text, err
		}
		ephemeral = true
	}
	if !ephemeral && !hostActionProof(in) && in.Sender != "" && in.Mode != "proactive" && (in.Source == nil || in.Source.EventID == "") {
		var err error
		in, err = w.recoverReplySource(ctx, in)
		if err != nil {
			return in, text, err
		}
	}
	if in.ReplyContext == nil {
		d, err := engine.ReviewGroundedReply(ctx, in, groundingDraft(text, legacyRecovered))
		if err != nil {
			return in, text, err
		}
		if d.Action != "reply" || len(d.Messages) != 1 || len(d.ReplyContexts) != 1 {
			return in, text, errors.New("reply_grounding_unavailable")
		}
		text = d.Messages[0]
		rc := d.ReplyContexts[0]
		in.ReplyContext = &rc
	} else if w.bindingEvidenceMissing(in) || bindingNeedsRecovery(in) && !(ephemeral && in.ReplyContext.State == "ephemeral_context" && in.ReplyContext.ContextVersion != "") {
		var err error
		if !ephemeral && !(in.Sender == "" && in.ReplyContext.State == "system_context") {
			in, err = w.recoverReplySource(ctx, in)
			if err != nil {
				return in, text, err
			}
		}
		// Re-evaluate the old draft using the verified original source. Do not
		// copy a current revision onto an unverified historical binding.
		in.ReplyContext = nil
		d, err := engine.ReviewGroundedReply(ctx, in, groundingDraft(text, true))
		if err != nil {
			return in, text, err
		}
		if d.Action != "reply" || len(d.Messages) != 1 || len(d.ReplyContexts) != 1 {
			return in, text, errors.New("reply_grounding_unavailable")
		}
		text = d.Messages[0]
		rc := d.ReplyContexts[0]
		in.ReplyContext = &rc
	}
	rc := *in.ReplyContext
	if in.QuoteSharedMessage {
		rc.QuoteRequired = true
	}
	in.ReplyContext = &rc
	if err := validateGroundedText(ctx, engine, in, *in.ReplyContext, text); err != nil {
		return in, text, err
	}
	if in.ReplyContext.QuoteRequired {
		anchor := in.ReplyContext.AnchorMessageID
		if anchor != "" && (in.Source == nil || anchor != in.Source.MessageID) {
			m, err := w.source.Message(ctx, anchor)
			if err != nil || m.ID != anchor || m.ThreadID != in.Thread || m.AccountID != in.Account || m.SenderID == "" {
				return in, text, errors.New("reply_quote_identity_unverified")
			}
			source, err := w.quotedAnchorSource(ctx, in, anchor)
			if err != nil || source.Sender != m.SenderID {
				return in, text, errors.New("reply_quote_source_mismatch")
			}
			in.QuoteServerID = m.ServerID
		} else if in.Source != nil {
			in.QuoteServerID = in.Source.ServerID
		}
		if in.QuoteServerID == "" {
			return in, text, errors.New("reply_quote_source_unavailable")
		}
		in.QuoteSharedMessage = true
	}
	return in, text, nil
}

func validateGroundedText(ctx context.Context, engine replyGroundingEngine, in companion.Input, proof companion.ReplyContext, text string) error {
	if validator, ok := engine.(interface {
		ValidateReplyText(context.Context, companion.Input, companion.ReplyContext, string) error
	}); ok {
		return validator.ValidateReplyText(ctx, in, proof, text)
	}
	return engine.ValidateReplyContext(ctx, in, proof)
}

func replyProofMetadataMissing(r *companion.ReplyContext) bool {
	return r == nil || r.ContextVersion == "" || r.State == ""
}

// The anchor's author is a source identity, not necessarily the outgoing
// addressee: Alpha can say something explicitly to Beta and be quoted in our
// reply to Beta. Audience membership alone never proves an anchor is related.
func (w *Worker) quotedAnchorSource(ctx context.Context, in companion.Input, anchor string) (*companion.SourceRef, error) {
	r := in.ReplyContext
	if r == nil || r.AnchorEventID == "" {
		return nil, errors.New("reply_quote_source_unproven")
	}
	listed := false
	for _, id := range r.SourceEventIDs {
		if id == r.AnchorEventID {
			listed = true
		}
	}
	if !listed {
		return nil, errors.New("reply_quote_source_unproven")
	}
	if e, ok := w.model.(*companion.Engine); ok && e.Store != nil {
		ev, err := e.Store.ReadEvent(ctx, []string{companion.ScopeID(in.Channel, in.Account, in.Thread)}, r.AnchorEventID)
		if err != nil || ev.Trace != anchor || ev.Source == nil || ev.Source.Sender != ev.Sender {
			return nil, errors.New("reply_quote_source_unproven")
		}
		return ev.Source, nil
	}
	// Test/custom adapters may already supply a verified immutable quote ref.
	for _, source := range []*companion.SourceRef{in.Source, in.QuotedSource} {
		if source != nil && source.EventID == r.AnchorEventID && source.MessageID == anchor && source.Account == in.Account && source.Thread == in.Thread && source.Sender != "" {
			return source, nil
		}
	}
	return nil, errors.New("reply_quote_source_unproven")
}

func (w *Worker) bindingEvidenceMissing(in companion.Input) bool {
	r := in.ReplyContext
	e, ok := w.model.(*companion.Engine)
	if !ok || r == nil {
		return false
	}
	if e.Store != nil && r.EvidenceEpoch == "" {
		return true
	}
	provider, needsAuthorization := e.Tools.(interface{ MemoryResultAuthorization() string })
	return needsAuthorization && provider.MemoryResultAuthorization() != "" && r.EvidenceAuthorization == ""
}

func (w *Worker) validateExistingReplyEvidence(ctx context.Context, in companion.Input) error {
	r := in.ReplyContext
	e, ok := w.model.(*companion.Engine)
	if !ok || r == nil {
		return nil
	}
	if r.EvidenceEpoch != "" {
		if e.Store == nil {
			return errors.New("reply_evidence_epoch_unavailable")
		}
		epoch, err := e.Store.RecallEpoch(ctx)
		if err != nil || epoch != r.EvidenceEpoch {
			return errReplyEvidenceEpochChanged
		}
	}
	if r.EvidenceAuthorization != "" {
		provider, ok := e.Tools.(interface{ MemoryResultAuthorized(string) bool })
		if !ok || !provider.MemoryResultAuthorized(r.EvidenceAuthorization) {
			return errors.New("reply_evidence_authorization_changed")
		}
	}
	return nil
}

// refreshUnchangedReplyEvidence handles only a queued text bubble whose
// explicit sources still have their original revisions. A changed or missing
// source keeps the old proof invalid and follows the existing recovery path.
func (w *Worker) refreshUnchangedReplyEvidence(ctx context.Context, in companion.Input) (companion.Input, error) {
	r := in.ReplyContext
	e, ok := w.model.(*companion.Engine)
	if !ok || r == nil || r.EvidenceEpoch == "" || e.Store == nil {
		return in, nil
	}
	epoch, err := e.Store.RecallEpoch(ctx)
	if err != nil || epoch == r.EvidenceEpoch {
		return in, err
	}
	if len(r.SourceEventIDs) == 0 || len(r.SourceVersions) == 0 {
		return in, errReplyEvidenceEpochChanged
	}
	scope := companion.ScopeID(in.Channel, in.Account, in.Thread)
	for _, id := range r.SourceEventIDs {
		if _, bound := r.SourceVersions[id]; !bound {
			return in, errReplyEvidenceEpochChanged
		}
	}
	for id, version := range r.SourceVersions {
		event, readErr := e.Store.ReadEvent(ctx, []string{scope}, id)
		if readErr != nil || event.Revision != version || event.Role != "user" && event.Role != "admin_correction" {
			return in, errReplyEvidenceEpochChanged
		}
	}
	updated := *r
	updated.EvidenceEpoch = epoch
	updated.ContextVersion = companion.ReplyContextFingerprint(in, updated)
	in.ReplyContext = &updated
	return in, nil
}

func groundingDraft(text string, legacy bool) companion.Decision {
	d := companion.Decision{Action: "reply", Messages: []string{text}}
	if legacy {
		d.ReplyContexts = []companion.ReplyContext{{RiskFlags: []string{"legacy_reply_revalidation"}}}
	}
	return d
}

func (w *Worker) verifyEphemeralSource(ctx context.Context, in companion.Input) (companion.Input, error) {
	s := in.Source
	if s == nil || s.MessageID == "" || s.MessageID != in.TraceID || s.Account != in.Account || s.Thread != in.Thread || s.Sender != in.Sender {
		return in, errors.New("ephemeral_reply_source_unavailable")
	}
	m, err := w.source.Message(ctx, s.MessageID)
	if err != nil || m.ID != s.MessageID || m.AccountID != in.Account || m.ThreadID != in.Thread || m.SenderID != in.Sender {
		return in, errors.New("ephemeral_reply_source_changed")
	}
	copy := *s
	copy.Integrity = "verified"
	in.Source = &copy
	return in, nil
}

func bindingNeedsRecovery(in companion.Input) bool {
	r := in.ReplyContext
	if r == nil {
		return false
	}
	if r.State == "system_context" && (in.Sender == "" || hostActionProof(in)) && r.ContextVersion != "" {
		return false
	}
	if r.State != "verified" || r.ContextVersion == "" {
		return true
	}
	if in.Sender != "" && in.Mode != "proactive" && (in.Source == nil || in.Source.BodyHash == "" || len(r.SourceEventIDs) == 0) {
		return true
	}
	return false
}

func (w *Worker) recoverReplySource(ctx context.Context, in companion.Input) (companion.Input, error) {
	e, ok := w.model.(*companion.Engine)
	if !ok || e.Store == nil {
		return in, errors.New("legacy_reply_source_unavailable")
	}
	source, ok := e.Store.SourceForInput(in)
	if !ok || source.EventID == "" {
		return in, errors.New("legacy_reply_source_unavailable")
	}
	ev, err := e.Store.ReadEvent(ctx, []string{companion.ScopeID(in.Channel, in.Account, in.Thread)}, source.EventID)
	if err != nil || ev.Sender != in.Sender || ev.Trace != in.TraceID {
		return in, errors.New("legacy_reply_source_unavailable")
	}
	// A fresh Engine decision can carry its full immutable proof before the
	// caller has copied the canonical Source projection back into Input. The
	// event identity and conversation binding are sufficient. Revisions and
	// body projections are mutable views of the same native message.
	verifiedProof := in.ReplyContext != nil && !w.bindingEvidenceMissing(in) && in.ReplyContext.State == "verified" && e.ValidateReplyContext(ctx, in, *in.ReplyContext) == nil
	if in.Source != nil {
		if in.Source.EventID != "" && in.Source.EventID != source.EventID {
			return in, errors.New("legacy_reply_source_changed")
		}
	}
	if in.ReplyContext != nil {
		for _, id := range in.ReplyContext.SourceEventIDs {
			evidence, err := e.Store.ReadEvent(ctx, []string{ev.Scope}, id)
			if err != nil || evidence.Role != "user" && evidence.Role != "admin_correction" || evidence.Scope != ev.Scope {
				return in, errors.New("legacy_reply_evidence_changed")
			}
		}
	}
	source.Revision = ev.Revision
	in.Source = source
	if verifiedProof && in.MessageTime == "" {
		in.MessageTime = source.OccurredAt
	}
	return in, nil
}
