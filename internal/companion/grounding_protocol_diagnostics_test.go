package companion

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"wechat-bot/internal/deepseek"
)

func TestGroundingProtocolDiagnosticsSeparateCoverageEvidenceAndSchema(t *testing.T) {
	units := groundingReviewUnits([]string{"收到，谢谢。"})
	good := atomicTestWire(units, atomicNonfactual)
	for _, tc := range []struct{ name, wire, code string }{
		{"syntax", `{`, "review_json_invalid"},
		{"missing", `{}`, "review_units_missing"},
		{"old_protocol", `{"bubbles":[]}`, "review_schema_invalid"},
		{"count", `{"units":[]}`, "review_units_count_mismatch"},
		{"unknown_unit", `{"units":[{"unit_id":"U9","atoms":[]}]}`, "review_unit_unknown"},
		{"empty_atoms", `{"units":[{"unit_id":"U1","atoms":[]}]}`, "review_atoms_missing"},
		{"coverage", atomicTestWire(units, func(u GroundingReviewUnit) []GroundingAtom { u.Text = "收到"; return atomicNonfactual(u) }), "review_text_coverage_incomplete"},
		{"support", atomicTestWire(units, func(u GroundingReviewUnit) []GroundingAtom {
			return []GroundingAtom{{Span: u.Text, Mode: "assertion", Claim: "synthetic", Status: "supported", Supports: []GroundingSupport{{Ref: "E999", Quote: "private quote"}}, Reason: "synthetic"}}
		}), "review_support_reference_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeGroundingUnits(tc.wire, "fixed", units, nil, 1)
			var protocol *GroundingProtocolError
			if !errors.As(err, &protocol) || protocol.ReviewProtocolCode() != tc.code || err.Error() != "reply_grounding_verdict_invalid" {
				t.Fatalf("err=%v protocol=%+v", err, protocol)
			}
		})
	}
	if _, err := decodeGroundingUnits(good, "fixed", units, nil, 1); err != nil {
		t.Fatal(err)
	}
}

type validatedProtocolModel struct{ called bool }

func (*validatedProtocolModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", errors.New("unexpected plain chat")
}
func (*validatedProtocolModel) ChatGrounding(context.Context, string, []map[string]string, deepseek.GroundingOptions) (string, error) {
	return "", errors.New("validator not forwarded")
}
func (m *validatedProtocolModel) ChatGroundingValidated(_ context.Context, _ string, turns []map[string]string, _ deepseek.GroundingOptions, validate func(string) error) (string, error) {
	m.called = true
	var protocol *GroundingProtocolError
	if !errors.As(validate(`{}`), &protocol) || protocol.Code != "review_units_missing" {
		return "", errors.New("host did not validate exact review protocol")
	}
	wire := syntheticGroundingVerdict(turns, "reject")
	if err := validate(wire); err != nil {
		return "", err
	}
	return wire, nil
}
func TestGroundingForwardsHostValidatorAndAcceptsSemanticRejection(t *testing.T) {
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "member", "source", "刚挂掉。")
	m := &validatedProtocolModel{}
	e := &Engine{Store: s, Config: c, model: m}
	v, err := e.checkReplyGrounding(t.Context(), in, Decision{Action: "reply", Messages: []string{"刚挂掉。"}})
	if err != nil || !m.called || v.Verdict != "reject" {
		raw, _ := json.Marshal(v)
		t.Fatalf("called=%v verdict=%s err=%v", m.called, raw, err)
	}
}
