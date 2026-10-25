package companion

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"wechat-bot/internal/mcptools"
)

type batchModel struct {
	steps int
	batch string
}

func (m *batchModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", errors.New("must use native protocol")
}
func (m *batchModel) ChatFunctions(context.Context, string, []map[string]string, []mcptools.Tool) (string, error) {
	m.steps++
	if m.steps == 1 {
		return m.batch, nil
	}
	return `{"action":"reply","messages":["已核对"]}`, nil
}

type batchTools struct {
	queries []string
	fail    bool
}

func (*batchTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "web_search", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (p *batchTools) Call(_ context.Context, _ string, args json.RawMessage) (string, error) {
	var a struct{ Query string }
	_ = json.Unmarshal(args, &a)
	p.queries = append(p.queries, a.Query)
	if p.fail {
		return "", errors.New("offline")
	}
	return "source https://example.org/", nil
}
func TestNativeBatchCallsAreSerialBoundedAndReplanOnFailure(t *testing.T) {
	batch := `{"action":"tool_batch","calls":[{"action":"tool","tool":"web_search","arguments":{"query":"first"}},{"action":"tool","tool":"web_search","arguments":{"query":"second"}}]}`
	for _, tc := range []struct {
		name   string
		budget int
		fail   bool
		calls  int
	}{{"serial", 4, false, 2}, {"budget", 1, false, 1}, {"replan_on_failure", 4, true, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			model := &batchModel{batch: batch}
			tools := &batchTools{fail: tc.fail}
			engine, err := NewPreview(enabledConfig(), model, "global")
			if err != nil {
				t.Fatal(err)
			}
			engine.Tools = tools
			engine.MaxToolCalls = tc.budget
			_, err = engine.Respond(context.Background(), Input{Mode: "direct", Text: "帮我查资料", Preview: true})
			if err != nil || len(tools.queries) != tc.calls || tools.queries[0] != "first" {
				t.Fatal(err, tools.queries)
			}
			if tc.calls == 2 && (tools.queries[1] != "second" || model.steps != 2) {
				t.Fatal("order or extra generation", tools.queries, model.steps)
			}
		})
	}
	invalid := `{"action":"tool_batch","calls":[{"action":"tool","tool":"web_search","arguments":{}},{"action":"tool","tool":"unknown","arguments":{}}]}`
	model := &batchModel{batch: invalid}
	tools := &batchTools{}
	engine, _ := NewPreview(enabledConfig(), model, "global")
	engine.Tools = tools
	engine.MaxToolCalls = 4
	if _, err := engine.Respond(context.Background(), Input{Mode: "direct", Text: "检查", Preview: true}); err == nil || len(tools.queries) != 0 {
		t.Fatal("unvalidated batch partially executed", err)
	}
}
