package companion

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
	"wechat-bot/internal/deepseek"
)

type dedicatedGroundingModel struct {
	calls   int
	options []deepseek.GroundingOptions
}

func (*dedicatedGroundingModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", errors.New("plain Chat must not serve review")
}
func (m *dedicatedGroundingModel) ChatGrounding(_ context.Context, policy string, messages []map[string]string, options deepseek.GroundingOptions) (string, error) {
	m.calls++
	m.options = append(m.options, options)
	if policy == groundingCheckPolicy {
		verdict := "allow"
		if m.calls == 1 {
			verdict = "reject"
		}
		return syntheticGroundingVerdict(messages, verdict), nil
	}
	return `{"action":"reply","bubbles":[{"text":"通话结束了。想接着聊的话，我在。","route_id":"R1"}]}`, nil
}
func TestGroundingUsesDedicatedTransportAndIndependentConfiguredBudget(t *testing.T) {
	s, c, _ := memoryFixture(t)
	c.Grounding = &GroundingSettings{Enabled: true, Model: "deepseek-v4-pro", MaxTokens: 16000, TimeoutSeconds: 45, ReasoningEffort: "max"}
	in := groundedInput(t, s, c, "alpha", "dedicated-check", "刚挂掉。")
	model := &dedicatedGroundingModel{}
	e := &Engine{Store: s, Config: c, model: model}
	d, err := e.ReviewGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"这是需要独立核验的候选回复。"}, ReplyContexts: []ReplyContext{{RiskFlags: []string{"synthetic_review_budget_test"}}}})
	if err != nil || d.Action != "reply" || model.calls != 3 {
		t.Fatal(d, err, model.calls)
	}
	for _, options := range model.options {
		if options.Model != "deepseek-v4-pro" || options.MaxTokens != 16000 || options.Timeout != 45*time.Second || options.ReasoningEffort != "max" {
			t.Fatal("review used ordinary response budget", options)
		}
	}
	if e.groundingTransport() != "dedicated_structured_review" {
		t.Fatal("dedicated capability lost")
	}
}

func TestGroundingSettingsRoundTripAndInvalidExplicitValues(t *testing.T) {
	c := enabledConfig()
	c.Grounding = &GroundingSettings{Model: "deepseek-v4-pro", MaxTokens: 14000, TimeoutSeconds: 180, ReasoningEffort: "high"}
	raw, _ := json.Marshal(c)
	var restored Config
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	options, err := restored.GroundingOptions()
	if err != nil || options.Model != "deepseek-v4-pro" || options.MaxTokens != 14000 || options.Timeout != 180*time.Second {
		t.Fatal(options, err)
	}
	for _, bad := range []GroundingSettings{{MaxTokens: -1}, {TimeoutSeconds: -1}, {ReasoningEffort: "invented"}} {
		c.Grounding = &bad
		if _, err := c.GroundingOptions(); err == nil {
			t.Fatal("invalid explicit review configuration silently changed", bad)
		}
	}
	c.Grounding = nil
	options, err = c.GroundingOptions()
	if err != nil || options.Model != "" || options.MaxTokens != 8192 || options.Timeout != 120*time.Second || options.ReasoningEffort != "high" {
		t.Fatal(options, err)
	}
}

func TestLexicalRiskDoesNotOverrideSemanticallyScopedReview(t *testing.T) {
	s, c, _ := memoryFixture(t)
	groundedInput(t, s, c, "beta", "beta-call", "我刚和父亲视频结束。")
	in := groundedInput(t, s, c, "alpha", "reporter", "你爸刚视频，刚挂掉。")
	in.Addressees = []string{"beta"}
	model := &groundingSequenceModel{}
	e := &Engine{Store: s, Config: c, model: model}
	text := "你提到的“你爸刚视频”是在说谁的父亲？"
	d, err := e.ReviewGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{text}})
	if err != nil || d.Action != "reply" || d.Messages[0] != text || model.calls != 1 {
		t.Fatal("quoted clarification was vetoed by an unscoped substring", d, err, model.calls)
	}
}

func TestHostAggregatesPerBubbleReviewWithoutModelOverallVerdict(t *testing.T) {
	units := groundingReviewUnits([]string{"safe", "unsupported"})
	wire := atomicTestWire(units, func(unit GroundingReviewUnit) []GroundingAtom {
		if unit.BubbleIndex == 0 {
			return atomicNonfactual(unit)
		}
		return []GroundingAtom{{Span: unit.Text, Mode: "assertion", Claim: "Unsupported test assertion", Status: "unsupported", Supports: []GroundingSupport{}, Reason: "No source supports this assertion."}}
	})
	result, err := decodeGroundingVerdict(wire, "host-only-binding", []string{"safe", "unsupported"}, nil)
	if err != nil || result.CandidateID != "host-only-binding" || result.Verdict != "reject" {
		t.Fatal(result, err)
	}
}
