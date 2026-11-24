package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type failedFinalModel struct{}

func (failedFinalModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", errors.New("synthetic model unavailable")
}

type extraContextModel struct{ *capturePolicyModel }

func (*extraContextModel) AdditionalContextTokens(context.Context, []map[string]string) int {
	return 10000
}

type textOnlyExtraModel struct{ turns []map[string]string }

func (m *textOnlyExtraModel) Chat(_ context.Context, _ string, turns []map[string]string) (string, error) {
	m.turns = turns
	return `{"action":"silent","messages":[]}`, nil
}

func (*textOnlyExtraModel) AdditionalContextTokens(context.Context, []map[string]string) int {
	return 100000
}

func TestPlainChatIgnoresNativeOnlyProviderContext(t *testing.T) {
	model := &textOnlyExtraModel{}
	e, err := NewPreview(enabledConfig(), model, "role")
	if err != nil {
		t.Fatal(err)
	}
	e.MemoryOptions = func() MemorySettings {
		settings := MemoryDefaults()
		settings.ContextWindow, settings.OutputReserve = 50000, 100
		return settings
	}
	if _, err := e.Respond(t.Context(), Input{Mode: "direct", Text: "继续处理", Preview: true}); err != nil || model.turns == nil {
		t.Fatal("plain Chat was blocked by native-only media/reasoning estimate", err)
	}
}

func TestFinalResponseIgnoresNativeOnlyProviderContext(t *testing.T) {
	model := &extraContextModel{capturePolicyModel: &capturePolicyModel{}}
	e, err := NewPreview(enabledConfig(), model, "role")
	if err != nil {
		t.Fatal(err)
	}
	e.MemoryOptions = func() MemorySettings {
		settings := MemoryDefaults()
		settings.ContextWindow, settings.OutputReserve = 5000, 100
		return settings
	}
	r := &AgentRunner{Engine: e}
	if _, err := r.finalResponse(t.Context(), "role", []map[string]string{{"role": "user", "content": `{"kind":"current_message","text":"继续处理"}`}}, "tool_no_progress", "same page"); err != nil || model.turns == nil {
		t.Fatal("text-only final response was blocked by native-only media/reasoning estimate", err)
	}
}

func TestNativeContextCountsProviderAddedContent(t *testing.T) {
	model := &extraContextModel{capturePolicyModel: &capturePolicyModel{}}
	turns := []map[string]string{{"role": "user", "content": `{"kind":"current_message","text":"继续处理"}`}}
	if _, err := compactModelContext(t.Context(), model, "role", turns, nil, 5000, 100); err == nil || !strings.Contains(err.Error(), "model_context_window_exceeded") {
		t.Fatal("native request omitted provider-added context from its budget", err)
	}
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

func TestFinalResponseUsesConfiguredContextWindow(t *testing.T) {
	model := &capturePolicyModel{}
	e, err := NewPreview(enabledConfig(), model, "role")
	if err != nil {
		t.Fatal(err)
	}
	e.MemoryOptions = func() MemorySettings {
		settings := MemoryDefaults()
		settings.ContextWindow, settings.OutputReserve = 2200, 100
		return settings
	}
	turns := []map[string]string{{"role": "user", "content": `{"kind":"current_message","text":"继续处理"}`}}
	for i := 0; i < 12; i++ {
		if i == 6 {
			turns = append(turns, map[string]string{"role": "user", "content": `{"kind":"current_followup","text":"改用新方案"}`})
		}
		call, _ := json.Marshal(map[string]any{"action": "tool", "tool": "probe", "arguments": map[string]int{"page": i}})
		result, _ := json.Marshal(map[string]any{"tool": "probe", "source": "untrusted_tool_result", "result": strings.Repeat("旧结果", 300), "execution": map[string]string{"status": "succeeded", "result_id": fmt.Sprint(i)}})
		turns = append(turns, map[string]string{"role": "assistant", "content": string(call)}, map[string]string{"role": "user", "content": string(result)})
	}
	r := &AgentRunner{Engine: e}
	if _, err := r.finalResponse(t.Context(), "role", turns, "tool_no_progress", "同一页没有变化"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateContextWindow(model.system, model.turns, nil, 2200, 100); err != nil {
		t.Fatal(err)
	}
	joined := fmt.Sprint(model.turns)
	if !strings.Contains(joined, "current_message") || !strings.Contains(joined, "改用新方案") || !strings.Contains(joined, "execution_feedback") || !strings.Contains(joined, "result_id") {
		t.Fatal("final response lost current request, follow-up, feedback or receipt")
	}
}
