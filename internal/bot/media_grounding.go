package bot

import (
	"context"
	"errors"
	"wechat-bot/internal/companion"
)

func restoreOriginInputTrace(in *companion.Input) {
	if in == nil || in.TraceID != "" {
		return
	}
	if in.Source != nil && in.Source.MessageID != "" {
		in.TraceID = in.Source.MessageID
	} else if in.OriginTraceID != "" {
		in.TraceID = in.OriginTraceID
	} else if in.ReplyContext != nil && in.ReplyContext.State == "system_context" {
		in.TraceID = in.ReplyContext.AnchorMessageID
	}
}

func hostActionProof(in companion.Input) bool {
	return in.Mode == "admin_action" && in.ReplyContext != nil && in.ReplyContext.State == "system_context" && in.ReplyContext.ContextVersion != ""
}

// Media has no user-visible text to rewrite. Bind the same immutable source
// proof as text replies, without generating a caption or calling the model.
func (w *Worker) prepareMediaEvidence(ctx context.Context, in companion.Input) (companion.Input, error) {
	w, scopeErr := w.deliveryEvidenceReader(in)
	if scopeErr != nil {
		return in, scopeErr
	}
	e, ok := w.model.(*companion.Engine)
	if !ok {
		return in, errors.New("media_source_proof_unavailable")
	}
	if err := w.validateExistingReplyEvidence(ctx, in); err != nil {
		return in, err
	}
	ephemeral := false
	if !e.Config.Permissions.Remember && in.Sender != "" && in.Mode != "proactive" && !hostActionProof(in) {
		var err error
		in, err = w.verifyEphemeralSource(ctx, in)
		if err != nil {
			return in, err
		}
		ephemeral = true
	}
	if !ephemeral && !hostActionProof(in) && in.Sender != "" && in.Mode != "proactive" && (in.Source == nil || in.Source.EventID == "" || in.ReplyContext != nil && (bindingNeedsRecovery(in) || w.bindingEvidenceMissing(in))) {
		var err error
		in, err = w.recoverReplySource(ctx, in)
		if err != nil {
			return in, err
		}
	}
	if in.ReplyContext != nil {
		return in, e.ValidateReplyContext(ctx, in, *in.ReplyContext)
	}
	r, err := e.BindReplyEvidence(ctx, in)
	if err != nil {
		return in, err
	}
	in.ReplyContext = &r
	return in, nil
}

func (w *Worker) validateMediaEvidence(ctx context.Context, d *mediaDelivery) error {
	in := d.Input
	if d.CrossRequester != "" {
		if d.OriginInput == nil {
			return errors.New("media_origin_proof_unavailable")
		}
		in = *d.OriginInput
		restoreOriginInputTrace(&in)
	}
	w, scopeErr := w.deliveryEvidenceReader(in)
	if scopeErr != nil {
		return scopeErr
	}
	e, ok := w.model.(*companion.Engine)
	if !ok || in.ReplyContext == nil || w.bindingEvidenceMissing(in) {
		return errors.New("media_source_proof_unavailable")
	}
	if !e.Config.Permissions.Remember && in.Sender != "" && in.Mode != "proactive" && !hostActionProof(in) {
		var err error
		in, err = w.verifyEphemeralSource(ctx, in)
		if err != nil {
			return err
		}
	}
	return e.ValidateReplyContext(ctx, in, *in.ReplyContext)
}
