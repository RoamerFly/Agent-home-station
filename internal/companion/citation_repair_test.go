package companion

import (
	"context"
	"encoding/json"
	"testing"
)

// This model supplies explicit synthetic verdicts, not semantic acceptance.
// It exercises the production editor/final-check flow with real quote checks.
type citationRepairModel struct {
	checks, edits int
	finalInvalid  bool
}

func (m *citationRepairModel) Chat(_ context.Context, policy string, turns []map[string]string) (string, error) {
	if policy != groundingCheckPolicy {
		m.edits++
		return `{"action":"reply","bubbles":[{"text":"视频展示煮面。","route_id":"R1"}]}`, nil
	}
	m.checks++
	var packet struct {
		Units []GroundingReviewUnit  `json:"review_units"`
		Rows  []GroundingEvidenceRow `json:"evidence_table"`
	}
	json.Unmarshal([]byte(turns[0]["content"]), &packet)
	var row GroundingEvidenceRow
	for _, r := range packet.Rows {
		if r.Kind == "user_utterance" {
			row = r
			break
		}
	}
	quote := row.Text
	if m.checks == 1 || m.finalInvalid {
		quote = "这句不存在于本次合成来源"
	}
	return atomicTestWire(packet.Units, func(u GroundingReviewUnit) []GroundingAtom {
		return []GroundingAtom{{Span: u.Text, Mode: "assertion", Claim: "视频展示煮面", Status: "supported", Supports: []GroundingSupport{{Ref: row.Ref, Quote: quote}}, Reason: "Synthetic explicit verdict"}}
	}), nil
}

func TestInvalidCitationUsesOneEditorAndIndependentFinalCheck(t *testing.T) {
	for _, badFinal := range []bool{false, true} {
		s, c, _ := memoryFixture(t)
		in := groundedInput(t, s, c, "member", "video-citation", "视频展示煮面。")
		m := &citationRepairModel{finalInvalid: badFinal}
		e := &Engine{Store: s, Config: c, model: m}
		d, err := e.DiagnoseGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"视频主要展示煮面。"}, ReplyContexts: []ReplyContext{{RiskFlags: []string{"incoming_media"}}}})
		if err != nil || m.checks != 2 || m.edits != 1 {
			t.Fatalf("review did not follow reject/edit/final-check: %+v %v checks=%d edits=%d", d, err, m.checks, m.edits)
		}
		if badFinal {
			if d.Action != "silent" || d.Reason != "grounding_check_rejected" || len(d.Messages) != 0 {
				t.Fatalf("invalid final citation was sent: %+v", d)
			}
			if d.ReviewFailure == nil || len(d.ReviewFailure.ViolationKinds) != 1 || d.ReviewFailure.ViolationKinds[0] != "invalid_evidence_quote" {
				t.Fatal("final rejection lost its exact safe cause", d.ReviewFailure)
			}
		} else if d.Action != "reply" || len(d.Messages) != 1 || d.Messages[0] != "视频展示煮面。" {
			t.Fatalf("repaired verified reply was lost or partitioned: %+v", d)
		}
	}
}
