package companion

import (
	"testing"
	"time"
)

// These synthetic adapters verify which private calls enter review. Their
// configured verdict is not evidence that a real model understands the date.
func privateTemporalFixture(t *testing.T, source string) (*Engine, Input, *groundingSequenceModel, *privacyTools) {
	t.Helper()
	s, c, in := memoryFixture(t)
	c.Timezone = "Asia/Shanghai"
	in.Thread, in.Sender, in.Text = "private-member", "alpha", source
	in = syntheticNativeInput(in, "private-time-source")
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	in.Text = "你还记得我的出行计划吗？"
	in = syntheticNativeInput(in, "private-time-current")
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	model := &groundingSequenceModel{}
	tools := &privacyTools{needed: true}
	return &Engine{Store: s, Config: c, model: model, Tools: tools}, in, model, tools
}

func TestPrivateTemporalReviewOnlyForAddedSpecificity(t *testing.T) {
	for _, tc := range []struct {
		name, source, draft string
		calls               int
	}{
		{"ordinary", "我准备星期六出行。", "收到。", 0},
		{"same-precision", "我准备星期六出行。", "你准备星期六出行。", 0},
		{"added-week", "我准备星期六出行。", "你准备下周六出行。", 1},
		{"explicit-date", "我准备2026年11月7日出行。", "你准备2026年11月7日出行。", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, in, model, tools := privateTemporalFixture(t, tc.source)
			out, err := e.DiagnoseGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{tc.draft}})
			if err != nil || out.Action != "reply" || out.Messages[0] != tc.draft || model.calls != tc.calls {
				t.Fatal(out, err, model.calls)
			}
			if len(out.ReplyContexts) != 1 {
				t.Fatal("private structural evidence missing", out)
			}
			if model.calls > 0 && model.inputs[0]["privacy_review"] != false {
				t.Fatal("private time review inherited group privacy restrictions", model.inputs[0])
			}
			if tools.report != "" {
				t.Fatal("legitimate private reply was moved elsewhere", tools.report)
			}
		})
	}
}

func TestPrivateTemporalReviewerKeepsAuthorizedDiagnostics(t *testing.T) {
	e, in, model, tools := privateTemporalFixture(t, "我准备星期六检查这项任务。")
	draft := "API与模型环境诊断已经记录，计划下周六检查。"
	out, err := e.DiagnoseGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{draft}})
	if err != nil || out.Action != "reply" || out.Messages[0] != draft || model.calls != 1 || model.inputs[0]["privacy_review"] != false || tools.report != "" {
		t.Fatal(out, err, model.calls, tools.report)
	}
}
