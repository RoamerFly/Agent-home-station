package companion

import (
	"context"
	"encoding/json"
	"testing"
	"wechat-bot/internal/mcptools"
)

type toolFixture struct{ calls int }

func (t *toolFixture) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "web_search", Description: "search", InputSchema: json.RawMessage("{}")}}, nil
}
func (t *toolFixture) Call(context.Context, string, json.RawMessage) (string, error) {
	t.calls++
	return "source https://example.org/ correct result", nil
}

type toolModel struct {
	steps  int
	repeat bool
	name   string
}

func (m *toolModel) Chat(_ context.Context, _ string, turns []map[string]string) (string, error) {
	m.steps++
	if m.steps == 1 || m.repeat {
		return "{" + `"action":"tool","tool":"` + m.name + `","arguments":{"query":"public topic"}}`, nil
	}
	return `{"action":"reply","messages":["结果来自 https://example.org/"]}`, nil
}
func TestAutonomousToolLoopAndBudget(t *testing.T) {
	c := enabledConfig()
	model := &toolModel{name: "web_search"}
	tools := &toolFixture{}
	e, err := NewPreview(c, model, "global")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = tools
	e.MaxToolCalls = 2
	d, err := e.Respond(context.Background(), Input{Mode: "direct", Text: "查一下", Preview: true})
	if err != nil || d.Action != "reply" || tools.calls != 1 || model.steps != 2 {
		t.Fatal("search continuation failed", d, err)
	}
	model = &toolModel{name: "web_search", repeat: true}
	e.model = model
	tools.calls = 0
	d, err = e.Respond(context.Background(), Input{Mode: "direct", Text: "loop", Preview: true})
	if err != nil || d.Action != "silent" || tools.calls != 1 {
		t.Fatal("tool budget ignored", d, err, tools.calls)
	}
	model = &toolModel{name: "unconfigured_tool", repeat: true}
	e.model = model
	tools.calls = 0
	d, err = e.Respond(context.Background(), Input{Mode: "direct", Text: "tool", Preview: true})
	if err != nil || d.Action != "silent" || tools.calls != 0 {
		t.Fatal("unknown tool invoked", d, err)
	}
}
