package companion

import (
	"context"
	"time"
)

// ExperienceReflectionSelection is an optional, model-owned choice made at
// the end of an authorised proactive turn. The Store checks the selected
// observation's scope, revision and hash again before saving the note.
type ExperienceReflectionSelection struct {
	// Choice is a one-based, host-issued position in the candidates provided to
	// this exact model turn.  The model never copies opaque source identifiers.
	Choice         int    `json:"choice,omitempty"`
	Note           string `json:"note,omitempty"`
	WantsToRevisit bool   `json:"wants_to_revisit,omitempty"`
	// Feeling is a short, self-described present mood. It is intentionally
	// separate from a sourced interest: it may be saved on its own and expires
	// from proactive context instead of turning into a permanent preference.
	Feeling string `json:"feeling,omitempty"`
}

type experienceReflectionCandidatePrompt struct {
	Choice        int    `json:"choice"`
	Summary       string `json:"summary"`
	MediaKind     string `json:"media_kind"`
	ObservedRange string `json:"observed_range"`
}

type experienceReflectionPrompt struct {
	Note          string `json:"note"`
	MediaKind     string `json:"media_kind,omitempty"`
	ObservedRange string `json:"observed_range,omitempty"`
}

func (e *Engine) proactiveReflectionContext(ctx context.Context, in Input, now time.Time) ([]ObservedExperienceReflectionCandidate, []ExperienceReflection, *ExperienceMood) {
	if !eligibleExperienceReflectionTurn(in) || !e.Config.Permissions.Remember || e.Store == nil || e.Store.db == nil {
		return nil, nil, nil
	}
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	candidates, err := e.Store.ListObservedExperienceReflectionCandidates(ctx, scope, 3)
	if err != nil && e.ToolLog != nil {
		e.ToolLog("experience_reflection_candidates_unavailable")
	}
	page, err := e.Store.ListExperienceReflections(ctx, scope, ExperienceReflectionQuery{Limit: 3, InterestOnly: true})
	if err != nil {
		if e.ToolLog != nil {
			e.ToolLog("experience_reflections_unavailable")
		}
		return candidates, nil, nil
	}
	mood, err := e.Store.RecentExperienceMood(ctx, scope, now.UTC())
	if err != nil && e.ToolLog != nil {
		e.ToolLog("experience_mood_unavailable")
	}
	return candidates, page.Items, mood
}

// eligibleExperienceReflectionTurn excludes continuations, recovery results
// and previews even when their caller labels them proactive.  A reflection is
// a decision made during an independent, authorised conversation evaluation;
// it must not leak into an old task's retry or turn a completed tool result
// into a fresh personal experience.
func eligibleExperienceReflectionTurn(in Input) bool {
	return in.Mode == "proactive" && !in.Surf && !in.Preview && in.TaskID == "" && !in.BackgroundResult && !in.ReportOnly &&
		!in.HistoricalReview && len(in.UnreadMessages) == 0 && len(in.RecoveryToolResults) == 0 && in.RecoveryEvidence == nil && in.RecoveryPrivacy == nil
}

func reflectionCandidatePrompts(candidates []ObservedExperienceReflectionCandidate) []experienceReflectionCandidatePrompt {
	out := make([]experienceReflectionCandidatePrompt, 0, len(candidates))
	for index, candidate := range candidates {
		out = append(out, experienceReflectionCandidatePrompt{Choice: index + 1, Summary: candidate.Summary, MediaKind: candidate.MediaKind, ObservedRange: candidate.ObservedRange})
	}
	return out
}

func reflectionPrompts(reflections []ExperienceReflection) []experienceReflectionPrompt {
	out := make([]experienceReflectionPrompt, 0, len(reflections))
	for _, reflection := range reflections {
		row := experienceReflectionPrompt{Note: reflection.Note}
		if reflection.Source != nil {
			row.MediaKind, row.ObservedRange = reflection.Source.MediaKind, reflection.Source.ObservedRange
		}
		out = append(out, row)
	}
	return out
}

// saveProactiveReflection is best-effort by design: an invalid optional note
// must never block the ordinary reply which was already produced by the same
// model turn.  The Store repeats exact scope, source and observation checks so
// a stale or forgotten candidate cannot be revived here.
func (e *Engine) saveProactiveReflection(ctx context.Context, in Input, candidates []ObservedExperienceReflectionCandidate, selection *ExperienceReflectionSelection) {
	if selection == nil || selection.Choice < 1 || selection.Choice > len(candidates) {
		return
	}
	candidate := candidates[selection.Choice-1]
	input := ExperienceReflectionInput{
		Scope:                    ScopeID(in.Channel, in.Account, in.Thread),
		SourceEventID:            candidate.SourceEventID,
		SourceEventRevision:      candidate.SourceEventRevision,
		SourceAssetID:            candidate.SourceAssetID,
		SourceObservationVersion: candidate.SourceObservationVersion,
		SourceObservationHash:    candidate.SourceObservationHash,
		Feeling:                  selection.Feeling,
		WantsToRevisit:           selection.WantsToRevisit,
		Note:                     selection.Note,
	}
	_, err := e.Store.SaveExperienceReflection(ctx, input)
	if err != nil && e.ToolLog != nil {
		e.ToolLog("experience_reflection_not_saved")
	}
}
