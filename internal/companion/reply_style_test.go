package companion

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGroupTurnSeesOwnStyleWithoutExtraModelReview(t *testing.T) {
	c := enabledConfig()
	c.Permissions.Remember = true
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m := &fakeModel{answer: `{"action":"reply","messages":["祝你们早日如愿。"]}`}
	e, err := New(c, s, m, "role")
	if err != nil {
		t.Fatal(err)
	}
	in := syntheticNativeInput(Input{Account: "a", Channel: "wechat", Thread: "room", Sender: "a1", IsGroup: true, Directed: true, Mode: "direct", Text: "我也期待有这么一天。"}, "first")
	now := time.Now()
	if err := s.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	for k, text := range []string{"哈哈，跑得像风一样[得意]", "哈哈，跑得比以前还快[得意]"} {
		if err := s.AcceptedDelivery(in, text, []string{"receipt1", "receipt2"}[k], c, now); err != nil {
			t.Fatal(err)
		}
	}
	in = syntheticNativeInput(Input{Account: "a", Channel: "wechat", Thread: "room", Sender: "a1", IsGroup: true, Directed: true, Mode: "direct", Text: "希望如此。"}, "next")
	d, err := e.Respond(t.Context(), in)
	if err != nil || len(d.Messages) != 1 || m.calls != 1 || !strings.Contains(m.system, "期待、类比或假设") {
		t.Fatalf("normal group reply %v, calls %d, err %v", d, m.calls, err)
	}
	if !strings.Contains(m.turns[len(m.turns)-1]["content"], `"recent_reply_style":["opening","expression","phrasing"]`) {
		t.Fatal("missing scoped repetition signals")
	}
}

func TestRecentReplyStyleUsesDistinctAcceptedBubbles(t *testing.T) {
	group := Input{IsGroup: true}
	first := Turn{Role: "assistant", DeliveryID: "a", Text: "哈哈，跑得像一阵风[得意]"}
	second := Turn{Role: "assistant", DeliveryID: "b", Text: "哈哈，跑得比上回还快[得意]"}
	scope := Scope{Messages: []Turn{first, second}}
	if got := recentReplyStyle(scope, group); !slices.Equal(got, []string{"opening", "expression", "phrasing"}) {
		t.Fatalf("paraphrased comparison missed: %v", got)
	}
	for _, other := range []Turn{first, {Role: "user", Text: second.Text}, {Role: "assistant", Text: second.Text}, {Role: "assistant", DeliveryID: "c", Text: second.Text, Media: &MediaMemory{Kind: "image"}}} {
		if got := recentReplyStyle(Scope{Messages: []Turn{first, other}}, group); len(got) != 0 {
			t.Fatalf("non-independent delivery counted: %v", got)
		}
	}
	for _, in := range []Input{{}, {IsGroup: true, SourceOnlyContext: true}} {
		if got := recentReplyStyle(scope, in); len(got) != 0 {
			t.Fatalf("unexpected scope: %v", got)
		}
	}
}

func TestNaturalReplyGuidanceReachesNormalAndFailureModelWithoutRewriting(t *testing.T) {
	// These are synthetic model outputs, not live-model quality evidence.
	answer := `{"action":"reply","messages":["找到原因了，第二段卡在上传。我正在接着处理。"]}`
	m := &fakeModel{answer: answer}
	e, err := NewPreview(enabledConfig(), m, "role")
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.Respond(context.Background(), Input{Mode: "direct", Text: "怎么没发过来？", Preview: true})
	if err != nil || !strings.Contains(m.system, "【自然说话】") || len(d.Messages) != 1 || d.Messages[0] != "找到原因了，第二段卡在上传。我正在接着处理。" {
		t.Fatal(d, err)
	}
	runner := &AgentRunner{Engine: e}
	d, err = runner.finalResponse(context.Background(), "role", nil, "upload_failed", "second part waiting")
	if err != nil || !strings.Contains(m.system, "【自然说话】") || len(d.Messages) != 1 {
		t.Fatal(d, err)
	}
	// Style is model guidance, not a deletion rule: an explicit user question
	// about distinctions may still need those words. Preserve model authorship.
	m.answer = `{"action":"reply","messages":["你问的这两件事不等于同一种情况。"]}`
	d, err = runner.finalResponse(context.Background(), "role", nil, "read_ok", "synthetic distinction")
	if err != nil || d.Messages[0] != "你问的这两件事不等于同一种情况。" {
		t.Fatal("post-generation word filter changed model output", d, err)
	}
}

func TestCharacterSingingAndVideoSummaryGuidanceReachesModel(t *testing.T) {
	m := &fakeModel{answer: `{"action":"reply","messages":["这段讲的是母子渐渐分离的过程，最后妈妈说舍不得。"]}`}
	e, err := NewPreview(enabledConfig(), m, "role")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Respond(context.Background(), Input{Mode: "direct", Text: "视频讲了什么？", Preview: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"角色演唱与视频内容", "第一人称", "原唱资源不冒充", "主要事件或观点", "不要为了接梗", "判断留在action/reason", "决定安静就用silent"} {
		if !strings.Contains(m.system, part) {
			t.Fatalf("missing guidance %s", part)
		}
	}
	// The policy asks the model to decide and write. It is never a post-hoc
	// text filter, including when a user actually asks for the production method.
	m.answer = `{"action":"reply","messages":["这次是用我的角色声线换声翻唱的。"]}`
	r := &AgentRunner{Engine: e}
	d, err := r.finalResponse(context.Background(), "role", nil, "singing_ready", "角色声线已转换完成")
	if err != nil || len(d.Messages) != 1 || d.Messages[0] != "这次是用我的角色声线换声翻唱的。" {
		t.Fatal(d, err)
	}
}
