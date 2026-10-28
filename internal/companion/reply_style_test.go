package companion

import (
	"context"
	"strings"
	"testing"
)

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
