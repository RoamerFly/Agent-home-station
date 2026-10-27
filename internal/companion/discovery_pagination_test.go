package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"wechat-bot/internal/mcptools"
)

type pagedCatalog struct{ calls int }

func (*pagedCatalog) List(context.Context) ([]mcptools.Tool, error) {
	tools := []mcptools.Tool{}
	for i := 0; i < 12; i++ {
		tools = append(tools, mcptools.Tool{Name: fmt.Sprintf("catalog_%02d", i), Description: "catalog inspection", InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
	return tools, nil
}
func (p *pagedCatalog) Call(context.Context, string, json.RawMessage) (string, error) {
	p.calls++
	return "real fixture execution", nil
}

type catalogModel struct {
	t    *testing.T
	step int
}

func (*catalogModel) Chat(context.Context, string, []map[string]string) (string, error) {
	panic("native functions required")
}
func (m *catalogModel) ChatFunctions(_ context.Context, _ string, _ []map[string]string, tools []mcptools.Tool) (string, error) {
	m.step++
	has := func(name string) bool {
		for _, t := range tools {
			if t.Name == name {
				return true
			}
		}
		return false
	}
	switch m.step {
	case 1:
		return `{"action":"tool","tool":"discover_tools","arguments":{"query":"catalog","limit":7}}`, nil
	case 2:
		if !has("catalog_06") || has("catalog_11") {
			m.t.Fatal("first discovery page leaked other schemas")
		}
		return `{"action":"tool","tool":"discover_tools","arguments":{"query":"catalog","offset":7,"limit":7}}`, nil
	case 3:
		if !has("catalog_11") {
			m.t.Fatal("later tool schema remained hidden")
		}
		return `{"action":"tool","tool":"catalog_11","arguments":{}}`, nil
	default:
		return `{"action":"reply","messages":["已核对"]}`, nil
	}
}
func TestNativeToolDiscoveryContinuesBeyondFirstEightMatches(t *testing.T) {
	tools := &pagedCatalog{}
	model := &catalogModel{t: t}
	e, _ := NewPreview(enabledConfig(), model, "policy")
	e.Tools = tools
	e.MaxToolCalls = 6
	if _, err := e.Respond(context.Background(), Input{Mode: "direct", Text: "核对能力目录", Preview: true}); err != nil || tools.calls != 1 {
		t.Fatal(err, tools.calls)
	}
}
