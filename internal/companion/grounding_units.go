package companion

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Review units are host-owned text partitions, not inferred propositions. Full
// coverage proves only that the protocol accounted for the candidate text;
// semantic atomization and entailment remain model judgments, not formal proof.
type GroundingReviewUnit struct {
	ID          string `json:"id"`
	BubbleIndex int    `json:"bubble_index"`
	Text        string `json:"text"`
}

type GroundingSupport struct {
	Ref   string `json:"ref"`
	Quote string `json:"quote"`
}

type GroundingAtom struct {
	Span               string             `json:"span"`
	Mode               string             `json:"mode"`
	Claim              string             `json:"claim"`
	Status             string             `json:"status"`
	NonfactualCategory string             `json:"nonfactual_category"`
	Supports           []GroundingSupport `json:"supports"`
	Reason             string             `json:"reason"`
}

type GroundingUnitVerdict struct {
	UnitID string          `json:"unit_id"`
	Atoms  []GroundingAtom `json:"atoms"`
}

func groundingReviewUnits(messages []string) []GroundingReviewUnit {
	out := []GroundingReviewUnit{}
	for bubble, text := range messages {
		start := 0
		var closing []rune
		flush := func(end int) {
			if strings.TrimSpace(text[start:end]) != "" {
				out = append(out, GroundingReviewUnit{ID: fmt.Sprintf("U%d", len(out)+1), BubbleIndex: bubble, Text: text[start:end]})
			}
			start = end
		}
		for offset, r := range text {
			switch r {
			case '“':
				closing = append(closing, '”')
			case '「':
				closing = append(closing, '」')
			case '『':
				closing = append(closing, '』')
			case '"':
				if len(closing) > 0 && closing[len(closing)-1] == '"' {
					closing = closing[:len(closing)-1]
				} else {
					closing = append(closing, '"')
				}
			default:
				if len(closing) > 0 && closing[len(closing)-1] == r {
					closing = closing[:len(closing)-1]
				}
			}
			if len(closing) == 0 && strings.ContainsRune("。！？!?；;\n", r) {
				flush(offset + utf8.RuneLen(r))
			}
		}
		flush(len(text))
	}
	return out
}

// GroundingProtocolError exposes only a fixed validation code and positional
// indices. It deliberately excludes model text, quotes and provider error bodies.
// Error retains the legacy code for callers that persisted it before diagnostics.
type GroundingProtocolError struct {
	Code      string
	UnitIndex int
	AtomIndex int
}

func (e *GroundingProtocolError) Error() string              { return "reply_grounding_verdict_invalid" }
func (e *GroundingProtocolError) ReviewProtocolCode() string { return e.Code }

func decodeGroundingUnits(answer, id string, units []GroundingReviewUnit, table []GroundingEvidenceRow, messageCount int) (GroundingVerdict, error) {
	unitIndex, atomIndex := 0, 0
	fail := func(code string) (GroundingVerdict, error) {
		return GroundingVerdict{}, &GroundingProtocolError{Code: code, UnitIndex: unitIndex, AtomIndex: atomIndex}
	}
	// Match the dedicated review transport's explicit response resource bound.
	// A caller-selected output budget must not inherit a short-chat parser cap.
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
		Units []GroundingUnitVerdict `json:"units"`
	}
	decoder := json.NewDecoder(bytes.NewBufferString(answer))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF {
		return fail("review_schema_invalid")
	}
	if wire.Units == nil {
		return fail("review_units_missing")
	}
	if len(wire.Units) != len(units) {
		return fail("review_units_count_mismatch")
	}
	byID := map[string]GroundingReviewUnit{}
	for _, unit := range units {
		byID[unit.ID] = unit
	}
	evidence := map[string]GroundingEvidenceRow{}
	for _, row := range table {
		evidence[row.Ref] = row
	}
	out := GroundingVerdict{CandidateID: id, Verdict: "allow", Bubbles: make([]GroundingBubbleVerdict, messageCount)}
	for i := range out.Bubbles {
		out.Bubbles[i] = GroundingBubbleVerdict{Index: i, Verdict: "allow", Violations: []GroundingViolation{}}
	}
	seen := map[string]bool{}
	for ui, result := range wire.Units {
		unitIndex, atomIndex = ui+1, 0
		unit, ok := byID[result.UnitID]
		if !ok {
			return fail("review_unit_unknown")
		}
		if seen[result.UnitID] {
			return fail("review_unit_duplicate")
		}
		if len(result.Atoms) == 0 {
			return fail("review_atoms_missing")
		}
		seen[result.UnitID] = true
		covered := make([]bool, len(unit.Text))
		bubble := &out.Bubbles[unit.BubbleIndex]
		for ai, atom := range result.Atoms {
			atomIndex = ai + 1
			if atom.Span == "" || !strings.Contains(unit.Text, atom.Span) {
				return fail("review_atom_span_invalid")
			}
			if atom.Supports == nil {
				return fail("review_atom_supports_missing")
			}
			if strings.TrimSpace(atom.Reason) == "" {
				return fail("review_atom_reason_missing")
			}
			switch atom.Mode {
			case "assertion", "presupposition", "report", "quote", "conditional", "nonfactual":
			default:
				return fail("review_atom_mode_invalid")
			}
			for start := 0; start <= len(unit.Text)-len(atom.Span); {
				at := strings.Index(unit.Text[start:], atom.Span)
				if at < 0 {
					break
				}
				at += start
				for i := at; i < at+len(atom.Span); i++ {
					covered[i] = true
				}
				start = at + len(atom.Span)
			}
			refs := []string{}
			invalidQuote := false
			for _, support := range atom.Supports {
				row, exists := evidence[support.Ref]
				if !exists {
					return fail("review_support_reference_invalid")
				}
				if strings.TrimSpace(support.Quote) == "" {
					return fail("review_support_quote_invalid")
				}
				if !groundingEvidenceContains(row, support.Quote) {
					invalidQuote = true
					continue
				}
				refs = append(refs, row.SourceRefs...)
			}
			if atom.Status == "nonfactual" {
				invalidNonfactual := atom.Mode != "nonfactual" || len(atom.Supports) != 0 || atom.Claim != ""
				switch atom.NonfactualCategory {
				case "greeting", "open_question", "conditional_offer", "acknowledgment", "social_offer":
				default:
					invalidNonfactual = true
				}
				if invalidNonfactual {
					bubble.Verdict, out.Verdict = "reject", "reject"
					bubble.Violations = append(bubble.Violations, GroundingViolation{Span: atom.Span, Kind: "invalid_nonfactual_classification", SourceRefs: uniqueSortedEvidence(refs), Reason: "The proposed nonfactual classification contradicts its mode, claim, evidence or category. This span has not passed review; repair or remove unsupported content and independently review the new draft."})
				}
			} else {
				if atom.Mode == "nonfactual" || atom.NonfactualCategory != "" || strings.TrimSpace(atom.Claim) == "" {
					return fail("review_atom_claim_invalid")
				}
				switch atom.Status {
				case "supported":
					if len(atom.Supports) == 0 {
						return fail("review_supported_without_evidence")
					}
				case "unsupported", "uncertain":
					bubble.Verdict, out.Verdict = "reject", "reject"
					bubble.Violations = append(bubble.Violations, GroundingViolation{Span: atom.Span, Kind: groundingAtomViolationKind(atom), SourceRefs: uniqueSortedEvidence(refs), Reason: atom.Reason})
				default:
					return fail("review_atom_status_invalid")
				}
			}
			if invalidQuote {
				// A well-formed but invented/paraphrased citation is a failed
				// evidence check, not a JSON problem. Reject this immutable draft
				// and let the existing single editor fix its claims/citations;
				// never resample a format verdict or count the citation as support.
				bubble.Verdict, out.Verdict = "reject", "reject"
				bubble.Violations = append(bubble.Violations, GroundingViolation{Span: atom.Span, Kind: "invalid_evidence_quote", SourceRefs: uniqueSortedEvidence(refs), Reason: "The supplied quotation is not present in the referenced evidence row. Recheck the actual source; do not treat a paraphrased or invented quotation as support."})
			}
		}
		for i, r := range unit.Text {
			if unicode.IsSpace(r) || unicode.IsPunct(r) {
				continue
			}
			for at := i; at < i+utf8.RuneLen(r); at++ {
				if !covered[at] {
					return fail("review_text_coverage_incomplete")
				}
			}
		}
		out.Units = append(out.Units, result)
	}
	return out, nil
}

func groundingAtomViolationKind(atom GroundingAtom) string {
	if atom.Mode == "presupposition" {
		return "presupposition"
	}
	return "unsupported_fact"
}

func groundingEvidenceContains(row GroundingEvidenceRow, quote string) bool {
	structured := row.Kind == "execution_result" || row.Kind == "host_knowledge" || row.Kind == "source_contract" || row.Kind == "role_contract" || row.Kind == "temporal_contract" || row.Kind == "quote_contract" || row.Kind == "derived_summary"
	if !structured && strings.Contains(row.Text, quote) {
		return true
	}
	if structured {
		var content any
		decoder := json.NewDecoder(strings.NewReader(row.Text))
		decoder.UseNumber()
		if decoder.Decode(&content) == nil && groundingValueContains(content, quote) {
			return true
		}
	}
	// Metadata is also evidence (e.g. sender, observed range, message time or
	// receipt state). Only actual values may be quoted, never JSON field names,
	// punctuation or strings fabricated across serialization boundaries.
	return groundingValueContains(row.Metadata, quote)
}

func groundingValueContains(value any, quote string) bool {
	switch v := value.(type) {
	case map[string]any:
		for _, item := range v {
			if groundingValueContains(item, quote) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if groundingValueContains(item, quote) {
				return true
			}
		}
	case []string:
		for _, item := range v {
			if strings.Contains(item, quote) {
				return true
			}
		}
	case string:
		return strings.Contains(v, quote)
	case json.Number:
		return v.String() == quote
	case bool, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		raw, _ := json.Marshal(v)
		return string(raw) == quote
	}
	return false
}
