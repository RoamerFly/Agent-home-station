package companion

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type failedFinalModel struct{}

func (failedFinalModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", errors.New("synthetic model unavailable")
}

func TestFailureTextIsModelAuthoredAndModelOutageProducesNoChat(t *testing.T) {
	m := &capturePolicyModel{}
	e, _ := NewPreview(enabledConfig(), m, "role")
	r := &AgentRunner{Engine: e}
	d, err := r.finalResponse(context.Background(), "role", nil, "download_failed", "connection refused")
	if err != nil || d.Action != "silent" || len(d.Messages) != 0 || !strings.Contains(m.turns[len(m.turns)-1]["content"], "connection refused") {
		t.Fatal("model did not receive actual failure or silence was overridden", d, err)
	}
	answer := `{"action":"reply","messages":["刚才那个地址连不上，你有别的版本吗？"]}`
	e.model = &fakeModel{answer: answer}
	d, err = r.finalResponse(context.Background(), "role", nil, "download_failed", "connection refused")
	if err != nil || len(d.Messages) != 1 || d.Messages[0] != "刚才那个地址连不上，你有别的版本吗？" {
		t.Fatal("program replaced model-authored text", d, err)
	}
	e.model = failedFinalModel{}
	d, err = r.finalResponse(context.Background(), "role", nil, "download_failed", "connection refused")
	if err == nil || len(d.Messages) != 0 {
		t.Fatal("model outage synthesized a chat reply", d, err)
	}
}
