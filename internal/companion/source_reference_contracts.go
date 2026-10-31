package companion

import (
	"context"
	"errors"
)

// SourceContractViolation is a bounded output/source inconsistency. It never
// asserts the opposite real-world fact or changes an authorization principal.
type SourceContractViolation struct {
	Code       string   `json:"code"`
	Span       string   `json:"span"`
	Kind       string   `json:"kind"`
	SourceRefs []string `json:"source_refs"`
	Reason     string   `json:"reason"`
}

type indexedSourceContractViolation struct {
	MessageIndex int `json:"message_index"`
	SourceContractViolation
}

func (e *Engine) sourceReferenceViolations(ctx context.Context, in Input, r ReplyContext, text string) []SourceContractViolation {
	issues := e.validateParticipationReference(ctx, in, r, text)
	return append(issues, e.validateMediaAnnotation(ctx, in, r, text)...)
}

func (e *Engine) sourceReferenceIssues(ctx context.Context, in Input, d Decision) []indexedSourceContractViolation {
	issues := []indexedSourceContractViolation{}
	for i, text := range d.Messages {
		if i >= len(d.ReplyContexts) {
			continue
		}
		for _, issue := range e.sourceReferenceViolations(ctx, in, d.ReplyContexts[i], text) {
			issues = append(issues, indexedSourceContractViolation{i, issue})
		}
	}
	return issues
}

func (e *Engine) validateSourceReferenceText(ctx context.Context, in Input, r ReplyContext, text string) error {
	if issues := e.sourceReferenceViolations(ctx, in, r, text); len(issues) > 0 {
		return errors.New(issues[0].Code)
	}
	return nil
}

// Contract metadata is derived from the current authorized sources only. It is
// neither an event ledger nor a new fact written into conversational memory.
func (e *Engine) sourceReferenceEvidence(ctx context.Context, in Input, d Decision) map[string]any {
	participation := []ParticipationReferenceEvidence{}
	for _, r := range d.ReplyContexts {
		item := e.participationReferenceEvidence(ctx, in, r)
		if item.Active {
			participation = append(participation, item)
		}
	}
	return map[string]any{"participation_reference": participation, "media_annotation": e.mediaAnnotationEvidence(ctx, in), "interpretation": "Bounded source/reference output contracts. Unknown participation is not nonparticipation; machine observations are not native upload annotations. They do not grant permissions or create persistent facts."}
}

func (e *Engine) currentReferenceRoles(ctx context.Context, in Input, d Decision) []map[string]any {
	roles := []map[string]any{}
	for i, r := range d.ReplyContexts {
		item := e.participationReferenceEvidence(ctx, in, r)
		if !item.Active {
			continue
		}
		roles = append(roles, map[string]any{"message_index": i, "request_actor_id": in.Sender, "focus": item.Focus, "event_id": item.Source.EventID, "source_revision": item.Source.Revision, "established_event_participant_ids": item.EstablishedParticipantIDs, "target_roles": item.TargetRoles, "reporter_role_does_not_establish_participation_or_personal_experience": true, "instruction": "Respond about the explicitly sourced third party or ask an open clarification. Do not make the reporting/quoting target a participant or invent their sensations, setting or feelings."})
	}
	return roles
}

func (e *Engine) enforceSourceReferenceVerdict(ctx context.Context, in Input, d Decision, verdict GroundingVerdict) GroundingVerdict {
	issues := e.sourceReferenceIssues(ctx, in, d)
	if len(issues) == 0 {
		return verdict
	}
	byIndex := map[int][]GroundingViolation{}
	for _, issue := range issues {
		byIndex[issue.MessageIndex] = append(byIndex[issue.MessageIndex], GroundingViolation{Span: issue.Span, Kind: issue.Kind, SourceRefs: issue.SourceRefs, Reason: issue.Reason})
	}
	for i := range verdict.Bubbles {
		if entries := byIndex[verdict.Bubbles[i].Index]; len(entries) > 0 {
			verdict.Bubbles[i].Verdict = "reject"
			verdict.Bubbles[i].Violations = append(verdict.Bubbles[i].Violations, entries...)
		}
	}
	verdict.Verdict = "reject"
	return verdict
}

func sourceReferenceEvidenceIDs(contracts map[string]any) []string {
	refs := []string{}
	if items, ok := contracts["participation_reference"].([]ParticipationReferenceEvidence); ok {
		for _, item := range items {
			if item.Source.EventID != "" && item.Source.Revision > 0 && validScopeID(item.Source.Scope) {
				refs = append(refs, item.Source.EventID)
			}
		}
	}
	if items, ok := contracts["media_annotation"].([]MediaAnnotationEvidence); ok {
		for _, item := range items {
			if item.EventID != "" && item.Revision > 0 && item.Availability == "authorized_full_source" && validScopeID(item.Scope) {
				refs = append(refs, item.EventID)
			}
		}
	}
	return uniqueSortedEvidence(refs)
}
