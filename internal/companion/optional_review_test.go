package companion

import "testing"

func TestDefaultReplyUsesIdentityChecksWithoutAdditionalModelCall(t *testing.T) {
	if Defaults().GroundingConfig().Enabled {
		t.Fatal("additional model review must be opt-in")
	}
	s, c, _ := memoryFixture(t)
	c.Grounding = nil
	in := groundedInput(t, s, c, "alpha", "default-no-review", "总结群里的视频。")
	model := &dedicatedGroundingModel{}
	e := &Engine{Store: s, Config: c, model: model}
	d, err := e.ReviewGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"收到，我先整理。"}})
	if err != nil || d.Action != "reply" || model.calls != 0 || len(d.ReplyContexts) != 1 {
		t.Fatal("ordinary reply required another model call", d, err, model.calls)
	}
	if err := e.ValidateReplyText(t.Context(), in, d.ReplyContexts[0], d.Messages[0]); err != nil {
		t.Fatal("deterministic binding was lost", err)
	}
	if err := s.Clear(ScopeID(in.Channel, in.Account, in.Thread)); err != nil {
		t.Fatal(err)
	}
	stale, err := e.ReviewGroundedReply(t.Context(), in, d)
	if err == nil && stale.Action == "reply" {
		t.Fatal("disabled semantic review bypassed invalidated source proof")
	}
}

func TestDisabledReviewDoesNotRerouteProgressThroughModel(t *testing.T) {
	e := &Engine{Config: Defaults(), model: &dedicatedGroundingModel{}}
	text, err := e.reviewProgressPrivacy(t.Context(), []map[string]string{{"role": "user", "content": `{"kind":"current_message","is_group":true}`}}, "wechat_read", "正在读取已授权的群历史。")
	if err != nil || text != "正在读取已授权的群历史。" {
		t.Fatal("progress unexpectedly needed a semantic reviewer", text, err)
	}
}
