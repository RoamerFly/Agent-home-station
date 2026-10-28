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
