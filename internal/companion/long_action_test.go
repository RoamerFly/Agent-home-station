package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"wechat-bot/internal/mcptools"
)

type longActionModel struct {
	t *testing.T
	n int
}

func (m *longActionModel) Chat(_ context.Context, system string, turns []map[string]string) (string, error) {
	m.n++
	found := false
	size := len(system)
	for _, v := range turns {
		size += len(v["content"])
		if strings.Contains(v["content"], `"kind":"current_message"`) {
			found = true
		}
	}
	if !found || len(turns) > 101 || size > 192<<10 {
		m.t.Fatalf("lost original request or exceeded bounds round=%d turns=%d bytes=%d", m.n, len(turns), size)
	}
	if m.n <= 30 {
		return fmt.Sprintf(`{"action":"tool","tool":"probe","arguments":{"step":%d}}`, m.n), nil
	}
	return `{"action":"reply","messages":["所有动作完成"]}`, nil
}

type longActionTools struct{ n int }

func (t *longActionTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "probe", Description: strings.Repeat("d", 44000), InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (t *longActionTools) Call(context.Context, string, json.RawMessage) (string, error) {
	t.n++
	return strings.Repeat("result ", 850), nil
}
func TestThirtyActionStepsKeepOriginalRequestWithoutReplayingTools(t *testing.T) {
	m := &longActionModel{t: t}
	tools := &longActionTools{}
	e, _ := NewPreview(enabledConfig(), m, "global")
	e.Tools = tools
	e.MaxToolCalls = 30
	turns := []map[string]string{}
	for i := 0; i < 30; i++ {
		turns = append(turns, map[string]string{"role": "user", "content": strings.Repeat("history ", 120)})
	}
	turns = append(turns, map[string]string{"role": "user", "content": `{"kind":"current_message","text":"请完成三十步测试"}`})
	d, err := e.decide(context.Background(), "system", turns)
	if err != nil || d.Action != "reply" || tools.n != 30 || m.n != 31 {
		t.Fatal(d, err, tools.n, m.n)
	}
}
