package companion

import (
	"context"
	"errors"

	"wechat-bot/internal/deepseek"
)

// GenerationEvidence binds a model result to the evidence and permissions that
// were available when its input was assembled. It is supplied only by the host;
// neither model JSON nor a later send may mint a replacement for an old result.
type GenerationEvidence struct {
	RecallEpoch   string
	Authorization string
}

// GenerationEvidenceChangedError pauses only the unexecuted remainder. Returned
// tool calls keep their durable result/task/idempotency identities; this is not
// a statement that those calls failed or that they should be replayed.
type GenerationEvidenceChangedError struct {
	Stage          string   `json:"stage"`
	CompletedTools []string `json:"completed_tools,omitempty"`
	Continuation   string   `json:"continuation"`
}

func (*GenerationEvidenceChangedError) Error() string { return "generation_evidence_changed" }

func generationEvidenceChanged() error {
	return &GenerationEvidenceChangedError{
		Stage:        "evidence_validation",
		Continuation: "Reassemble current authorized sources before continuing. Inspect existing tool results using their original task/result/idempotency identities; do not replay returned calls.",
	}
}

// GenerationEvidence returns a value copy of the host's stamp. The unexported
// Decision field cannot be supplied by a provider's JSON.
func (d Decision) GenerationEvidence() (GenerationEvidence, bool) {
	if d.generationEvidence == nil {
		return GenerationEvidence{}, false
	}
	return *d.generationEvidence, true
}

// WithDecisionGenerationEvidence carries a completed decision's original stamp
// into text/media queue preparation. A missing legacy stamp remains missing.
func WithDecisionGenerationEvidence(ctx context.Context, d Decision) context.Context {
	if evidence, ok := d.GenerationEvidence(); ok {
		return withGenerationEvidence(ctx, evidence)
	}
	return ctx
}

func (d PendingReview) GenerationEvidence() (GenerationEvidence, bool) {
	if d.generationEvidence == nil {
		return GenerationEvidence{}, false
	}
	return *d.generationEvidence, true
}

func WithPendingReviewGenerationEvidence(ctx context.Context, d PendingReview) context.Context {
	if evidence, ok := d.GenerationEvidence(); ok {
		return withGenerationEvidence(ctx, evidence)
	}
	return ctx
}

// WithStoredGenerationEvidence attaches host-persisted review metadata. It is
// not a grant: consumers must call ValidateGenerationEvidence before use, and
// must never construct a replacement from current permissions for old work.
func WithStoredGenerationEvidence(ctx context.Context, evidence GenerationEvidence) context.Context {
	return withGenerationEvidence(ctx, evidence)
}

type generationEvidenceKey struct{}

// GenerationEvidenceFromContext exposes the host's original call stamp to tool
// adapters. In particular, an outbound tool must retain this stamp rather than
// signing its generated arguments against whatever happens to be current.
func GenerationEvidenceFromContext(ctx context.Context) (GenerationEvidence, bool) {
	evidence, ok := ctx.Value(generationEvidenceKey{}).(GenerationEvidence)
	return evidence, ok
}

func withGenerationEvidence(ctx context.Context, evidence GenerationEvidence) context.Context {
	return context.WithValue(ctx, generationEvidenceKey{}, evidence)
}

func (e *Engine) modelRequestContext(ctx context.Context, evidence GenerationEvidence) context.Context {
	return deepseek.WithRequestGuard(withGenerationEvidence(ctx, evidence), func(attempt context.Context) error {
		return e.ValidateGenerationEvidence(attempt, evidence)
	})
}

func (e *Engine) currentGenerationEvidence(ctx context.Context) (GenerationEvidence, error) {
	var evidence GenerationEvidence
	if err := ctx.Err(); err != nil {
		return evidence, err
	}
	if e.Store != nil && e.Store.db != nil {
		var err error
		evidence.RecallEpoch, err = e.Store.RecallEpoch(ctx)
		if err != nil {
			return GenerationEvidence{}, err
		}
	}
	if access, ok := e.Tools.(interface{ MemoryResultAuthorization() string }); ok {
		evidence.Authorization = access.MemoryResultAuthorization()
	}
	return evidence, nil
}

func (e *Engine) captureGenerationEvidence(ctx context.Context) (GenerationEvidence, error) {
	if previous, ok := GenerationEvidenceFromContext(ctx); ok {
		if err := e.ValidateGenerationEvidence(ctx, previous); err != nil {
			return GenerationEvidence{}, err
		}
		// Do not read and replace the validated stamp: a change between these
		// two reads would otherwise re-sign already assembled context.
		return previous, nil
	}
	return e.currentGenerationEvidence(ctx)
}

// ValidateGenerationEvidence never refreshes a stamp. A changed or withdrawn
// authorization invalidates a generated result even if its anchor still exists.
// Ordinary incoming/accepted messages do not change RecallEpoch.
func (e *Engine) ValidateGenerationEvidence(ctx context.Context, evidence GenerationEvidence) error {
	current, err := e.currentGenerationEvidence(ctx)
	if err != nil {
		return err
	}
	if current != evidence || evidence.Authorization != "" && !e.memoryResultAuthorized(evidence.Authorization) {
		return generationEvidenceChanged()
	}
	return e.validateGenerationSources(ctx)
}

// Cross-application sources can change independently of the conversation's
// RecallEpoch. Check the host's existing proofs before reusing model context
// or accepting actions derived from it.
func (e *Engine) validateGenerationSources(ctx context.Context) error {
	if scoped, ok := e.Tools.(interface{ ValidateGenerationSources(context.Context) error }); ok {
		if err := scoped.ValidateGenerationSources(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return generationEvidenceChanged()
		}
	}
	return nil
}

func (e *Engine) bindGenerationDecision(ctx context.Context, d Decision, evidence GenerationEvidence) (Decision, error) {
	if err := e.ValidateGenerationEvidence(ctx, evidence); err != nil {
		return Decision{}, err
	}
	if d.generationEvidence != nil && *d.generationEvidence != evidence {
		return Decision{}, generationEvidenceChanged()
	}
	d.generationEvidence = &evidence
	return d, nil
}

func generationPartialError(err error, completed []string) error {
	var changed *GenerationEvidenceChangedError
	if !errors.As(err, &changed) {
		return err
	}
	copy := *changed
	copy.Stage = "generation_paused"
	copy.CompletedTools = append([]string(nil), completed...)
	return &copy
}

// Resolving an ID is bound to the immutable message identity and conversation
// scope. Native transports may refresh timestamps, server ids, body projections
// and revisions while the same message is being re-read.
func validateResolvedGenerationSource(in Input, known, resolved *SourceRef) error {
	if resolved == nil {
		return generationEvidenceChanged()
	}
	if known != nil {
		for _, pair := range [][2]string{{known.InstanceID, resolved.InstanceID}, {known.EventID, resolved.EventID}, {known.MessageID, resolved.MessageID}, {known.Account, resolved.Account}, {known.Thread, resolved.Thread}, {known.Sender, resolved.Sender}} {
			if pair[0] != "" && pair[0] != pair[1] {
				return generationEvidenceChanged()
			}
		}
	}
	return nil
}

func (e *Engine) validateGenerationInputSource(ctx context.Context, in Input) error {
	source := in.Source
	if in.Preview || source == nil || source.EventID == "" || e.Store == nil || e.Store.db == nil {
		return nil
	}
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	event, err := e.Store.ReadEvent(ctx, []string{scope}, source.EventID)
	if err != nil || event.Source == nil || event.Scope != scope || event.Role != "user" || event.Sender != in.Sender || source.Account != "" && source.Account != in.Account || source.Thread != "" && source.Thread != in.Thread || event.ID != source.EventID {
		return generationEvidenceChanged()
	}
	if err := validateResolvedGenerationSource(in, source, event.Source); err != nil {
		return err
	}
	if source.MessageID != "" && source.MessageID != event.Trace {
		return generationEvidenceChanged()
	}
	return nil
}
