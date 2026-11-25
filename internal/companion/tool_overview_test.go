package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"wechat-bot/internal/mcptools"
)

type overviewAccess struct{ calls int }

func (*overviewAccess) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{
		{Name: "music_service_status", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "music_service_start", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "station_tool_status", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}, nil
}

func (a *overviewAccess) Call(context.Context, string, json.RawMessage) (string, error) {
	a.calls++
	return "unexpected host call", nil
}

func (*overviewAccess) ToolOverview(context.Context) (map[string]any, error) {
	return map[string]any{"music": "stopped"}, nil
}

type overviewModel struct {
	t     *testing.T
	steps int
}

func (*overviewModel) Chat(context.Context, string, []map[string]string) (string, error) {
	panic("native functions required")
}

func (m *overviewModel) ChatFunctions(_ context.Context, _ string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
	m.steps++
	if m.steps == 1 {
		for _, tool := range tools {
			if tool.Name == "tool_overview" {
				return `{"action":"tool","tool":"tool_overview","arguments":{}}`, nil
			}
		}
		m.t.Fatal("overview was not offered to native model")
	}
	feedback := turns[len(turns)-1]["content"]
	var wrapped struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(feedback), &wrapped); err != nil {
		m.t.Fatal(err)
	}
	for _, want := range []string{`"authorized_tool_count":3`, `"group":"music"`, `"music":"stopped"`, `"health_probed":false`} {
		if !strings.Contains(wrapped.Result, want) {
			m.t.Fatalf("overview missing %s: %s", want, feedback)
		}
	}
	return `{"action":"reply","messages":["已检查工具清单。"]}`, nil
}

func TestNativeToolOverviewUsesGrantedCatalogWithoutIndividualToolCalls(t *testing.T) {
	access := &overviewAccess{}
	model := &overviewModel{t: t}
	e, err := NewPreview(enabledConfig(), model, "policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools, e.MaxToolCalls = access, 2
	if _, err := e.Respond(t.Context(), Input{Mode: "direct", Text: "有哪些工具可用", Preview: true}); err != nil {
		t.Fatal(err)
	}
	if access.calls != 0 || model.steps != 2 {
		t.Fatalf("overview made host calls=%d model turns=%d", access.calls, model.steps)
	}
}
