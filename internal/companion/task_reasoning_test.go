package companion

import (
	"context"
	"strings"
	"testing"
	"wechat-bot/internal/deepseek"
)

type taskReasoningProbe struct {
	client     *deepseek.Client
	selectMode bool
	seen       []deepseek.TaskReasoning
	catalog    []string
}

func (m *taskReasoningProbe) SupportsTaskReasoning() bool { return true }
func (m *taskReasoningProbe) TaskReasoningSettings(ctx context.Context) (deepseek.TaskReasoning, error) {
	return m.client.TaskReasoningSettings(ctx)
}
func (m *taskReasoningProbe) Chat(ctx context.Context, system string, _ []map[string]string) (string, error) {
	p, err := m.client.TaskReasoningSettings(ctx)
	if err != nil {
		return "", err
	}
	m.seen = append(m.seen, p)
	m.catalog = append(m.catalog, system)
	if m.selectMode && len(m.seen) == 1 {
		return `{"action":"tool","tool":"task_reasoning","arguments":{"mode":"high","max_tokens":16384,"reason":"synthetic document reconstruction"}}`, nil
	}
	return `{"action":"reply","messages":["已完成合成检查"]}`, nil
}
func TestTaskReasoningAgentSelectionAndManualPrecedence(t *testing.T) {
	for _, mode := range []string{"auto", "max", "inherit"} {
		t.Run(mode, func(t *testing.T) {
			off := false
			client, err := deepseek.NewWithAPIKey(deepseek.Config{ThinkingEnabled: &off, MaxTokens: 768, ToolMaxTokens: 4096}, "synthetic-task-key")
			if err != nil {
				t.Fatal(err)
			}
			model := &taskReasoningProbe{client: client, selectMode: mode == "auto"}
			c := enabledConfig()
			c.TaskReasoning = mode
			e, err := NewPreview(c, model, "synthetic")
			if err != nil {
				t.Fatal(err)
			}
			tools := &toolFixture{}
			e.Tools = tools
			e.MaxToolCalls = 3
			d, err := e.Respond(t.Context(), Input{Mode: "direct", Text: "执行合成任务", Preview: true})
			if err != nil || d.Action != "reply" {
				t.Fatal(d, err)
			}
			if tools.calls != 0 {
				t.Fatal("reasoning control delegated to external tools")
			}
			last := model.seen[len(model.seen)-1]
			if mode == "auto" && (len(model.seen) != 2 || last.Mode != "high" || last.MaxTokens != 16384) {
				t.Fatal("agent profile did not affect next request", model.seen)
			}
			if mode == "max" && last.Mode != "max" {
				t.Fatal("manual override missing", last)
			}
			if mode == "inherit" && last.Mode != "off" {
				t.Fatal("daily settings changed", last)
			}
			if mode != "auto" && strings.Contains(model.catalog[0], `"name":"task_reasoning"`) {
				t.Fatal("agent can override manual policy")
			}
			daily, _ := client.TaskReasoningSettings(context.Background())
			if daily.Mode != "off" || daily.MaxTokens != 4096 {
				t.Fatal("temporary state leaked", daily)
			}
		})
	}
}
