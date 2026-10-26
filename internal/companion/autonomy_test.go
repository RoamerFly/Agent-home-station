package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"wechat-bot/internal/mcptools"
)

type feedbackProbe struct {
	count      int
	autonomous bool
	tool       string
}

func (p *feedbackProbe) AutonomousActions() bool { return p.autonomous }
func (p *feedbackProbe) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: p.tool, InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (p *feedbackProbe) Call(context.Context, string, json.RawMessage) (string, error) {
	p.count++
	if p.count == 1 {
		return "", fmt.Errorf("temporary failure")
	}
	return fmt.Sprintf(`{"step":%d,"state":"verified"}`, p.count), nil
}

type feedbackModel struct {
	n    int
	tool string
	t    *testing.T
}

func (m *feedbackModel) Chat(_ context.Context, _ string, turns []map[string]string) (string, error) {
	m.n++
	if m.n == 2 && !strings.Contains(turns[len(turns)-1]["content"], "失败") {
		m.t.Fatal("failure not returned to model")
	}
	if len(turns) > 95 {
		m.t.Fatal("unbounded transcript", len(turns))
	}
	if m.n <= 85 {
		return fmt.Sprintf(`{"action":"tool","tool":%q,"arguments":{"round":%d}}`, m.tool, m.n), nil
	}
	return `{"action":"reply","messages":["完成了"]}`, nil
}
func TestDeliveryAndAutonomousFeedbackLoopsExceedLegacyLimits(t *testing.T) {
	for _, autonomous := range []bool{false, true} {
		name := "wechat_delivery_retry"
		if autonomous {
			name = "server_read"
		}
		p := &feedbackProbe{autonomous: autonomous, tool: name}
		m := &feedbackModel{tool: name, t: t}
		e, _ := NewPreview(enabledConfig(), m, "")
		e.Tools = p
		e.MaxToolCalls = 1
		d, err := e.decide(context.Background(), "system", []map[string]string{{"role": "user", "content": `{"kind":"current_message","text":"持续处理并验证"}`}})
		if err != nil || d.Action != "reply" || p.count != 85 {
			t.Fatal(autonomous, d, err, p.count)
		}
	}
}
