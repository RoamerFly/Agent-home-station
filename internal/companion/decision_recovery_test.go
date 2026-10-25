package companion

import (
	"context"
	"strings"
	"testing"
)

func TestMalformedVideoDecisionCorrectsThenUsesRealTool(t *testing.T) {
	model := &sequenceModel{answers: []string{`{"action":"tool",`, `{"action":"tool","tool":"web_search","arguments":{"query":"超兽武装 动画片段"}}`, `{"action":"reply","messages":["找到了对应的公开页面：https://example.org/"]}`}}
	e, _ := NewPreview(enabledConfig(), model, "role")
	tools := &toolFixture{}
	e.Tools = tools
	e.MaxToolCalls = 2
	diagnostics := []DecisionDiagnostic{}
	ctx := WithDecisionObserver(context.Background(), func(d DecisionDiagnostic) { diagnostics = append(diagnostics, d) })
	d, err := e.Respond(ctx, Input{Mode: "direct", Preview: true, Text: "帮我找超兽武装的视频链接，只要链接"})
	if err != nil || d.Action != "reply" || tools.calls != 1 || len(diagnostics) != 2 || diagnostics[1].Code != "decision_format_recovered" {
		t.Fatal(d, err, diagnostics)
	}
	if strings.Contains(strings.Join(d.Messages, ""), "action") {
		t.Fatal("protocol exposed")
	}
}
func TestFormatRepairHasTwoAttemptBoundAndNoToolSideEffects(t *testing.T) {
	for _, answer := range []string{`{"action":"tool",`, `{"action":"tool","tool":"web_search","arguments":[]}`, `{"action":"nonsense"}`, `{"action":"reply","messages":["a","b","c","d"]}`, `<｜｜DSML｜｜ calls>broken`} {
		model := &sequenceModel{answers: []string{answer}}
		e, _ := NewPreview(enabledConfig(), model, "role")
		tools := &toolFixture{}
		e.Tools = tools
		e.MaxToolCalls = 5
		d, err := e.Respond(context.Background(), Input{Mode: "direct", Preview: true, Text: "你好"})
		if err != nil || d.Action != "silent" || len(model.turns) != 3 || tools.calls != 0 {
			t.Fatal("unbounded repair or effect", answer, d, len(model.turns), tools.calls, err)
		}
	}
}
func TestInvalidFinalDecisionRepairsWithoutRepeatingSuccessfulTool(t *testing.T) {
	model := &sequenceModel{answers: []string{`{"action":"tool","tool":"web_search","arguments":{"query":"动画"}}`, `{"action":"reply","messages":"坏格式"}`, `{"action":"reply","messages":["这是找到的页面 https://example.org/"]}`}}
	e, _ := NewPreview(enabledConfig(), model, "role")
	tools := &toolFixture{}
	e.Tools = tools
	e.MaxToolCalls = 1
	d, err := e.Respond(context.Background(), Input{Mode: "direct", Preview: true, Text: "找一下动画链接"})
	if err != nil || d.Action != "reply" || tools.calls != 1 || len(model.turns) != 3 {
		t.Fatal(d, err, tools.calls, len(model.turns))
	}
}
