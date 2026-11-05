package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// This adapter verifies assembly and read-only verdict handling, not natural
// language accuracy. The live fixed-draft tests independently measure that.
type scopedReviewProtocolModel struct {
	policy  string
	state   map[string]any
	verdict string
}

func (m *scopedReviewProtocolModel) Chat(_ context.Context, policy string, messages []map[string]string) (string, error) {
	m.policy = policy
	_ = json.Unmarshal([]byte(messages[0]["content"]), &m.state)
	return syntheticGroundingVerdict(messages, m.verdict), nil
}

func TestGroundingScopeProtocolPreservesEpistemicAndConditionalPairs(t *testing.T) {
	for _, tc := range []struct {
		name, source, draft, verdict string
	}{
		{"evidence_availability", "资料只记录了一扇灰色门。", "现有资料还无法核实门牌号。", "allow"},
		{"world_negation", "资料只记录了一扇灰色门。", "那扇门没有门牌。", "reject"},
		{"local_condition", "风扇停了。", "如果想休息，可以先歇一会。", "allow"},
		{"condition_does_not_cover_previous_claim", "风扇停了。", "桌面的纸多半不会动了吧。如果想休息，可以先歇一会。", "reject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c, _ := memoryFixture(t)
			in := groundedInput(t, s, c, "alpha", tc.name, tc.source)
			model := &scopedReviewProtocolModel{verdict: tc.verdict}
			e := &Engine{Store: s, Config: c, model: model}
			d, err := e.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{tc.draft}})
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(d)
			out, err := e.checkReplyGrounding(t.Context(), in, d)
			if err != nil || out.Verdict != tc.verdict {
				t.Fatal(out, err)
			}
			after, _ := json.Marshal(d)
			if string(before) != string(after) {
				t.Fatal("scope protocol changed the exact candidate")
			}
			scope, ok := model.state["evidence_scope"].(map[string]any)
			if !ok || scope["complete_external_world"] != false || scope["complete_history"] != false || scope["coverage"] != "bounded_authorized_current_request_snapshot" {
				t.Fatal("bounded evidence was mislabeled as complete", scope)
			}
			for _, principle := range []string{"外界事实承诺", "当前可见证据状态", "语法作用域", "后句的如果不改变前句"} {
				if !strings.Contains(model.policy, principle) {
					t.Fatal("review protocol omitted semantic scope", principle)
				}
			}
		})
	}
}

func TestGroundingEvidenceScopeNeverReplacesPositiveExecutionResult(t *testing.T) {
	e, in := executionFixture(t, map[string]any{"status": "accepted", "receipt_id": "confirmed-receipt", "target": "verified-target"})
	model := &scopedReviewProtocolModel{verdict: "reject"}
	e.model = model
	_, err := e.checkReplyGrounding(t.Context(), in, Decision{Action: "reply", Messages: []string{"目前未取得任何接受凭证。"}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(model.state["evidence_table"])
	if !strings.Contains(string(data), "confirmed-receipt") || !strings.Contains(string(data), "accepted") {
		t.Fatal("epistemic metadata hid an actual matching result", string(data))
	}
	if !strings.Contains(model.policy, "已有匹配结果或回执") {
		t.Fatal("evidence availability was allowed to contradict a known result")
	}
}
