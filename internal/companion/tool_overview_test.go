package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/mcptools"
)

type overviewAccess struct {
	calls int
	host  map[string]any
}

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

func (a *overviewAccess) ToolOverview(context.Context) (map[string]any, error) {
	if a.host != nil {
		return a.host, nil
	}
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
	if strings.Contains(wrapped.Result, `"names"`) || strings.Index(wrapped.Result, `"host"`) > strings.Index(wrapped.Result, `"groups"`) {
		m.t.Fatalf("default overview expanded names or buried host state: %s", wrapped.Result)
	}
	return `{"action":"reply","messages":["已检查工具清单。"]}`, nil
}

func TestToolOverviewNamesAreExplicitAndContinueBeforeFeedbackTruncation(t *testing.T) {
	e, err := NewPreview(enabledConfig(), &overviewModel{t: t}, "policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = &overviewAccess{}
	runner := &AgentRunner{Engine: e}
	granted := []mcptools.Tool{{Name: "music_service_start"}, {Name: "music_service_status"}}
	for i := 0; i < 250; i++ {
		granted = append(granted, mcptools.Tool{Name: fmt.Sprintf("catalog_%03d_%s", i, strings.Repeat("x", 40))})
	}
	defaultResult, err := runner.toolOverview(t.Context(), granted, json.RawMessage(`{}`))
	if err != nil || len(defaultResult) >= 5500 || strings.Contains(defaultResult, `"names"`) {
		t.Fatalf("default overview is not compact: bytes=%d err=%v", len(defaultResult), err)
	}
	selected, err := runner.toolOverview(t.Context(), granted, json.RawMessage(`{"group":"music","limit":1}`))
	if err != nil || !strings.Contains(selected, `"names":["music_service_start"]`) || !strings.Contains(selected, `"has_more":true`) {
		t.Fatalf("group selection did not page names: %s err=%v", selected, err)
	}
	wide, err := runner.toolOverview(t.Context(), granted, json.RawMessage(`{"include_names":true,"limit":250}`))
	var result struct {
		Names          []string       `json:"names"`
		Host           map[string]any `json:"host"`
		NextOffset     int            `json:"next_offset"`
		HasMore        bool           `json:"has_more"`
		EffectiveLimit int            `json:"effective_limit"`
	}
	if err != nil || len(wide) > 5500 || json.Unmarshal([]byte(wide), &result) != nil || len(result.Names) == 0 || !result.HasMore || result.NextOffset != len(result.Names) || result.EffectiveLimit != len(result.Names) || result.Host["music"] != "stopped" {
		t.Fatalf("wide overview lost host or continuation: bytes=%d err=%v", len(wide), err)
	}
}

func TestToolOverviewKeepsFifteenHostServicesWithinFeedbackBudget(t *testing.T) {
	e, err := NewPreview(enabledConfig(), &overviewModel{t: t}, "policy")
	if err != nil {
		t.Fatal(err)
	}
	services := make([]map[string]any, 15)
	for i := range services {
		services[i] = map[string]any{
			"id": fmt.Sprintf("service-%02d", i), "installed": true,
			"enabled": true, "state": "runtime_ready", "runtime_ready": true,
		}
	}
	e.Tools = &overviewAccess{host: map[string]any{
		"music_bridge":        map[string]any{"registered": true, "supervisor_running": true, "backend_probed": false, "start_tool": "music_service_start"},
		"workspace_execution": map[string]any{"mode": "local", "gpu_mounted_by_default": false, "host_gpu_probed": false},
		"station_catalog":     services,
	}}
	runner := &AgentRunner{Engine: e}
	granted := make([]mcptools.Tool, 250)
	for i := range granted {
		granted[i].Name = fmt.Sprintf("catalog_%03d_%s", i, strings.Repeat("x", 40))
	}
	for _, query := range []string{`{}`, `{"include_names":true,"limit":250}`} {
		result, err := runner.toolOverview(t.Context(), granted, json.RawMessage(query))
		if err != nil {
			t.Fatal(err)
		}
		feedback := runner.toolFeedback(t.Context(), "tool_overview", result, time.Now(), nil, false)
		var wrapped struct {
			Result string `json:"result"`
		}
		if err := json.Unmarshal([]byte(feedback), &wrapped); err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Host struct {
				StationCatalog []map[string]any `json:"station_catalog"`
			} `json:"host"`
		}
		if wrapped.Result != result || json.Unmarshal([]byte(wrapped.Result), &decoded) != nil || len(decoded.Host.StationCatalog) != 15 {
			t.Fatalf("overview clipped or lost services: query=%s bytes=%d feedback=%d", query, len(result), len(feedback))
		}
	}
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
