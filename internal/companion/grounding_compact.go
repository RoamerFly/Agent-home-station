package companion

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

// The host owns the complete sentence text and evidence snapshot. The reviewer
// judges every sentence as a whole, returning source IDs rather than copying
// spans, quotes and redundant classifications. This is a semantic model check,
// not a claim that a source ID mechanically proves entailment.
func decodeCompactGrounding(answer, id string, units []GroundingReviewUnit, table []GroundingEvidenceRow, count int) (GroundingVerdict, error) {
	position := 0
	fail := func(code string) (GroundingVerdict, error) {
		return GroundingVerdict{}, &GroundingProtocolError{Code: code, UnitIndex: position}
	}
	if len(answer) > 16<<20 {
		return fail("review_response_too_large")
	}
	if !utf8.ValidString(answer) {
		return fail("review_invalid_encoding")
	}
	if !json.Valid([]byte(answer)) {
		return fail("review_json_invalid")
	}
	var wire struct {
		Units []struct {
			ID     string   `json:"unit_id"`
			Status string   `json:"status"`
			Refs   []string `json:"source_refs"`
			Reason string   `json:"reason"`
		} `json:"units"`
	}
	d := json.NewDecoder(bytes.NewBufferString(answer))
	d.DisallowUnknownFields()
	if d.Decode(&wire) != nil || d.Decode(new(any)) != io.EOF {
		return fail("review_schema_invalid")
	}
	if wire.Units == nil {
		return fail("review_units_missing")
	}
	if len(wire.Units) != len(units) {
		return fail("review_units_count_mismatch")
	}
	byID := map[string]GroundingReviewUnit{}
	for _, u := range units {
		byID[u.ID] = u
	}
	evidence := map[string]GroundingEvidenceRow{}
	for _, row := range table {
		evidence[row.Ref] = row
	}
	out := GroundingVerdict{CandidateID: id, Verdict: "allow", Bubbles: make([]GroundingBubbleVerdict, count)}
	for i := range out.Bubbles {
		out.Bubbles[i] = GroundingBubbleVerdict{Index: i, Verdict: "allow", Violations: []GroundingViolation{}}
	}
	seen := map[string]bool{}
	for i, r := range wire.Units {
		position = i + 1
		u, ok := byID[r.ID]
		if !ok {
			return fail("review_unit_unknown")
		}
		if seen[r.ID] {
			return fail("review_unit_duplicate")
		}
		seen[r.ID] = true
		if strings.TrimSpace(r.Reason) == "" {
			return fail("review_atom_reason_missing")
		}
		if r.Refs == nil {
			return fail("review_atom_supports_missing")
		}
		refs := []string{}
		supports := []GroundingSupport{}
		for _, ref := range uniqueSortedEvidence(r.Refs) {
			row, exists := evidence[ref]
			if !exists {
				return fail("review_support_reference_invalid")
			}
			refs = append(refs, row.SourceRefs...)
			supports = append(supports, GroundingSupport{Ref: ref})
		}
		atom := GroundingAtom{Span: u.Text, Mode: "assertion", Claim: u.Text, Status: r.Status, Supports: supports, Reason: r.Reason}
		kind := ""
		switch r.Status {
		case "supported":
			if len(r.Refs) == 0 {
				return fail("review_supported_without_evidence")
			}
		case "nonfactual":
			// The compact review does not claim to classify a greeting vs an
			// invitation. Do not invent a legacy category for the audit record.
			atom.Mode, atom.Claim = "nonfactual", ""
			if len(r.Refs) != 0 {
				kind = "invalid_nonfactual_classification"
			}
		case "unsupported", "uncertain":
			kind = "unsupported_fact"
		default:
			return fail("review_atom_status_invalid")
		}
		if kind != "" {
			b := &out.Bubbles[u.BubbleIndex]
			b.Verdict, out.Verdict = "reject", "reject"
			b.Violations = append(b.Violations, GroundingViolation{Span: u.Text, Kind: kind, SourceRefs: uniqueSortedEvidence(refs), Reason: r.Reason})
		}
		out.Units = append(out.Units, GroundingUnitVerdict{UnitID: u.ID, Atoms: []GroundingAtom{atom}})
	}
	return out, nil
}
