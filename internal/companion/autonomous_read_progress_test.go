package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"wechat-bot/internal/mcptools"
)

type autonomousStatusTools struct {
	results []string
	calls   int
}

func (*autonomousStatusTools) AutonomousActions() bool { return true }
func (*autonomousStatusTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "music_local_status", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (t *autonomousStatusTools) Call(_ context.Context, name string, _ json.RawMessage) (string, error) {
	if name != "music_local_status" {
		return "", fmt.Errorf("unexpected tool %s", name)
	}
	index := min(t.calls, len(t.results)-1)
	t.calls++
	return t.results[index], nil
}

type autonomousStatusModel struct {
	statusRequests int
	finalCode      string
	stopAfter      int
}

func (m *autonomousStatusModel) Chat(_ context.Context, _ string, turns []map[string]string) (string, error) {
	var feedback struct {
		Kind string `json:"kind"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(turns[len(turns)-1]["content"]), &feedback); err != nil {
		return "", err
	}
	m.finalCode = feedback.Code
	return `{"action":"reply","messages":["已记录当前状态"]}`, nil
}

func (m *autonomousStatusModel) ChatFunctions(context.Context, string, []map[string]string, []mcptools.Tool) (string, error) {
	if m.statusRequests == 0 {
		m.statusRequests++
		return `{"action":"tool","tool":"discover_tools","arguments":{"query":"music_local_status"}}`, nil
	}
	if m.statusRequests >= m.stopAfter {
		return `{"action":"reply","messages":["状态已更新"]}`, nil
	}
	m.statusRequests++
	if m.statusRequests > 8 {
		return "", fmt.Errorf("autonomous status loop did not stop")
	}
	return `{"action":"tool","tool":"music_local_status","arguments":{"job_id":"same-job"}}`, nil
}

func TestAutonomousUnchangedStatusCheckpointsInsteadOfLooping(t *testing.T) {
	model := &autonomousStatusModel{stopAfter: 9}
	tools := &autonomousStatusTools{results: []string{`{"state":"running"}`}}
	e, err := NewPreview(enabledConfig(), model, "policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools, e.MaxToolCalls = tools, 1
	d, err := e.Respond(t.Context(), Input{Mode: "direct", Preview: true, Text: "查询原音乐任务"})
	if err != nil || d.Action != "reply" || tools.calls != 3 || model.finalCode != "tool_no_progress" {
		t.Fatalf("decision=%+v err=%v calls=%d final_code=%s", d, err, tools.calls, model.finalCode)
	}
}

func TestAutonomousChangedStatusCanContinue(t *testing.T) {
	model := &autonomousStatusModel{stopAfter: 5}
	tools := &autonomousStatusTools{results: []string{`{"state":"queued"}`, `{"state":"running"}`, `{"state":"completed"}`}}
	e, err := NewPreview(enabledConfig(), model, "policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools, e.MaxToolCalls = tools, 1
	d, err := e.Respond(t.Context(), Input{Mode: "direct", Preview: true, Text: "查询原音乐任务"})
	if err != nil || d.Action != "reply" || tools.calls != 4 || model.finalCode != "" {
		t.Fatalf("decision=%+v err=%v calls=%d final_code=%s", d, err, tools.calls, model.finalCode)
	}
}

type cyclingDiscoveryModel struct {
	autonomousStatusModel
	pages int
}

func (m *cyclingDiscoveryModel) ChatFunctions(context.Context, string, []map[string]string, []mcptools.Tool) (string, error) {
	m.pages++
	if m.pages > 6 {
		return "", fmt.Errorf("repeated discovery pages did not stop")
	}
	if m.pages%2 == 1 {
		return `{"action":"tool","tool":"discover_tools","arguments":{"query":"music_","offset":0,"limit":1}}`, nil
	}
	return `{"action":"tool","tool":"discover_tools","arguments":{"query":"music_","offset":1,"limit":1}}`, nil
}

type cyclingDiscoveryTools struct{ autonomousStatusTools }

func (*cyclingDiscoveryTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{
		{Name: "music_local_status", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "music_service_status", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}, nil
}

func TestAutonomousDiscoveryPageCycleCheckpoints(t *testing.T) {
	model := &cyclingDiscoveryModel{}
	e, err := NewPreview(enabledConfig(), model, "policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools, e.MaxToolCalls = &cyclingDiscoveryTools{}, 1
	d, err := e.Respond(t.Context(), Input{Mode: "direct", Preview: true, Text: "找音乐工具"})
	if err != nil || d.Action != "reply" || model.pages != 4 || model.finalCode != "tool_no_progress" {
		t.Fatalf("decision=%+v err=%v pages=%d final_code=%s", d, err, model.pages, model.finalCode)
	}
}
