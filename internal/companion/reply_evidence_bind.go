package companion

import "context"

// BindReplyEvidence creates transport-independent structural evidence for a
// newly authorized submission. It does not generate/edit text or call a model.
// Existing queued evidence is validated exactly as stored, never re-signed.
func (e *Engine) BindReplyEvidence(ctx context.Context, in Input) (ReplyContext, error) {
	if original, ok := GenerationEvidenceFromContext(ctx); ok {
		if err := e.ValidateGenerationEvidence(ctx, original); err != nil {
			return ReplyContext{}, err
		}
	}
	if in.ReplyContext != nil {
		if err := e.ValidateReplyContext(ctx, in, *in.ReplyContext); err != nil {
			return ReplyContext{}, err
		}
		return *cloneReplyContext(in.ReplyContext), nil
	}
	d, err := e.normalizeReplyContexts(ctx, in, Decision{Action: "reply", Messages: []string{""}})
	if err != nil {
		return ReplyContext{}, err
	}
	d, err = e.bindReplyEvidence(ctx, in, d)
	if err != nil {
		return ReplyContext{}, err
	}
	return *cloneReplyContext(&d.ReplyContexts[0]), nil
}
