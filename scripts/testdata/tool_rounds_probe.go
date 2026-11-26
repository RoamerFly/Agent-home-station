package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

// Schemas come from the real permission filter. Service operations run in memory.
// Only the two task tools may use the temporary fixture's Admin store.
type roundsSandbox struct {
	taskCall   func(context.Context, string, json.RawMessage) (string, error)
	catalog    []mcptools.Tool
	ready      bool
	operations []string
}

func (s *roundsSandbox) List(context.Context) ([]mcptools.Tool, error) {
	return slices.Clone(s.catalog), nil
}
func (s *roundsSandbox) ToolOverview(context.Context) (map[string]any, error) {
	return map[string]any{"synthetic_service": "stopped"}, nil
}
func (s *roundsSandbox) Call(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if s.taskCall != nil && (name == "agent_tasks" || name == "agent_task") {
		return s.taskCall(ctx, name, raw)
	}
	var p struct {
		Action string
		ToolID string `json:"tool_id"`
	}
	if name != "station_tool_manage" || json.Unmarshal(raw, &p) != nil || p.ToolID != "music-bridge" {
		return "", fmt.Errorf("unexpected sandbox operation %q", name)
	}
	s.operations = append(s.operations, p.Action)
	switch p.Action {
	case "ensure_ready":
		s.ready = true
		return `{"repair_state":"ready","tool":{"id":"music-bridge","ready":true}}`, nil
	case "status":
		return fmt.Sprintf(`{"id":"music-bridge","ready":%t}`, s.ready), nil
	default:
		return "", fmt.Errorf("unexpected synthetic action %q", p.Action)
	}
}

type roundsResult struct {
	Scenario       string   `json:"scenario"`
	Outcome        string   `json:"outcome"`
	Detail         string   `json:"result"`
	ModelRequests  int      `json:"model_requests"`
	Input          []int    `json:"estimated_input_by_request"`
	Total          int      `json:"total_estimated_input"`
	Feedback       []string `json:"successful_tool_feedback"`
	Terminals      []string `json:"observed_terminal_tools"`
	TerminalCount  int      `json:"observed_terminal_count"`
	ServiceActions []string `json:"simulated_service_actions,omitempty"`
}
type scriptedRounds struct {
	t    *testing.T
	row  *roundsResult
	host *roundsSandbox
}

func (m *scriptedRounds) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", fmt.Errorf("expected native tool dispatch")
}
func roundsTool(tools []mcptools.Tool, name string) (mcptools.Tool, bool) {
	for _, tool := range tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return mcptools.Tool{}, false
}
func roundsSupportsRecovery(tools []mcptools.Tool) bool {
	tool, ok := roundsTool(tools, "station_tool_manage")
	if !ok {
		return false
	}
	var schema struct {
		Properties map[string]struct{ Enum []string }
	}
	return json.Unmarshal(tool.InputSchema, &schema) == nil && slices.Contains(schema.Properties["action"].Enum, "ensure_ready") && slices.Contains(schema.Properties["action"].Enum, "status")
}
func (m *scriptedRounds) feedback(turns []map[string]string, want string) json.RawMessage {
	m.t.Helper()
	var feedback struct {
		Tool, Result string
		Execution    struct{ Status string }
	}
	if len(turns) == 0 || json.Unmarshal([]byte(turns[len(turns)-1]["content"]), &feedback) != nil || feedback.Tool != want || feedback.Execution.Status != "succeeded" || !json.Valid([]byte(feedback.Result)) {
		m.t.Fatalf("missing successful JSON feedback for %s", want)
	}
	m.row.Feedback = append(m.row.Feedback, want)
	return json.RawMessage(feedback.Result)
}
func (m *scriptedRounds) ChatFunctions(_ context.Context, system string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
	schemas, err := json.Marshal(tools)
	if err != nil {
		return "", err
	}
	tokens := companion.EstimateTokens(system) + companion.EstimateTokens(string(schemas))
	for _, turn := range turns {
		tokens += companion.EstimateTokens(turn["content"]) + 8
	}
	r := m.row
	r.Input = append(r.Input, tokens)
	r.Total += tokens
	r.ModelRequests++
	const finish = `{"action":"silent","messages":[],"reason":"synthetic_rounds_finished"}`
	if r.Scenario == "ordinary_chat" {
		r.Outcome, r.Detail = "passed", "finished_without_tools"
		return finish, nil
	}
	if r.Scenario == "tool_inventory" {
		return m.inventory(tools, turns, finish)
	}
	if r.Scenario == "task_continuation" {
		return m.continueTask(tools, turns, finish)
	}
	return m.recoverService(tools, turns, finish)
}

func (m *scriptedRounds) continueTask(tools []mcptools.Tool, turns []map[string]string, finish string) (string, error) {
	r := m.row
	switch r.ModelRequests {
	case 1:
		if _, ok := roundsTool(tools, "agent_tasks"); !ok {
			return "", fmt.Errorf("first request lacks task lookup")
		}
		return `{"action":"tool","tool":"agent_tasks","arguments":{"status":"active"}}`, nil
	case 2:
		var listing struct{ Items []agentTask }
		if json.Unmarshal(m.feedback(turns, "agent_tasks"), &listing) != nil || len(listing.Items) != 1 || listing.Items[0].ID != "rounds-original" || listing.Items[0].Checkpoint != "first_operation_verified" {
			return "", fmt.Errorf("original checkpoint not visible")
		}
		if _, ok := roundsTool(tools, "agent_task"); !ok {
			return "", fmt.Errorf("task update schema missing")
		}
		return `{"action":"tool","tool":"agent_task","arguments":{"id":"rounds-original","state":"waiting","checkpoint":"next_operation_pending","delay_seconds":90}}`, nil
	case 3:
		var saved struct{ Task agentTask }
		if json.Unmarshal(m.feedback(turns, "agent_task"), &saved) != nil || saved.Task.ID != "rounds-original" || saved.Task.Checkpoint != "next_operation_pending" || saved.Task.State != "waiting" {
			return "", fmt.Errorf("original checkpoint did not advance")
		}
		r.Outcome, r.Detail = "passed", "original_task_resumed_without_resubmitting_business_operation"
		return finish, nil
	}
	return "", fmt.Errorf("unexpected continuation round")
}

func (m *scriptedRounds) inventory(tools []mcptools.Tool, turns []map[string]string, finish string) (string, error) {
	r := m.row
	if r.ModelRequests == 1 {
		if _, ok := roundsTool(tools, "tool_overview"); !ok {
			r.Outcome, r.Detail = "unsupported", "tool_overview_not_offered"
			return finish, nil
		}
		return `{"action":"tool","tool":"tool_overview","arguments":{}}`, nil
	}
	if r.ModelRequests != 2 {
		return "", fmt.Errorf("unexpected inventory round")
	}
	var overview struct {
		AuthorizedToolCount int `json:"authorized_tool_count"`
		Host                map[string]string
	}
	if json.Unmarshal(m.feedback(turns, "tool_overview"), &overview) != nil || overview.AuthorizedToolCount != len(m.host.catalog) || overview.Host["synthetic_service"] != "stopped" {
		return "", fmt.Errorf("incomplete authorized inventory result")
	}
	r.Outcome, r.Detail = "passed", "authorized_catalog_and_stopped_service_observed"
	return finish, nil
}
func (m *scriptedRounds) recoverService(tools []mcptools.Tool, turns []map[string]string, finish string) (string, error) {
	r := m.row
	switch r.ModelRequests {
	case 1:
		if !roundsSupportsRecovery(m.host.catalog) {
			r.Outcome, r.Detail = "unsupported", "authorized_manage_schema_lacks_ensure_ready_or_status"
			return finish, nil
		}
		if _, ok := roundsTool(tools, "discover_tools"); !ok {
			return "", fmt.Errorf("native discovery missing")
		}
		return `{"action":"tool","tool":"discover_tools","arguments":{"query":"station_tool_manage","limit":1}}`, nil
	case 2:
		var page struct{ Matches []string }
		if json.Unmarshal(m.feedback(turns, "discover_tools"), &page) != nil || !slices.Contains(page.Matches, "station_tool_manage") || !roundsSupportsRecovery(tools) {
			return "", fmt.Errorf("discovery failed to expose the authorized schema")
		}
		return `{"action":"tool","tool":"station_tool_manage","arguments":{"action":"ensure_ready","tool_id":"music-bridge"}}`, nil
	case 3:
		var result struct {
			RepairState string `json:"repair_state"`
			Tool        struct{ Ready bool }
		}
		if json.Unmarshal(m.feedback(turns, "station_tool_manage"), &result) != nil || result.RepairState != "ready" || !result.Tool.Ready {
			return "", fmt.Errorf("repair feedback did not confirm readiness")
		}
		if !roundsSupportsRecovery(tools) {
			return "", fmt.Errorf("manage schema missing for follow-up")
		}
		return `{"action":"tool","tool":"station_tool_manage","arguments":{"action":"status","tool_id":"music-bridge"}}`, nil
	case 4:
		var status struct {
			ID    string
			Ready bool
		}
		if json.Unmarshal(m.feedback(turns, "station_tool_manage"), &status) != nil || status.ID != "music-bridge" || !status.Ready || !slices.Equal(m.host.operations, []string{"ensure_ready", "status"}) {
			return "", fmt.Errorf("independent status read did not confirm repair")
		}
		r.Outcome, r.Detail = "passed", "stopped_to_ready_verified_by_status"
		return finish, nil
	}
	return "", fmt.Errorf("unexpected recovery round")
}

func TestSyntheticNativeToolRounds(t *testing.T) {
	output := os.Getenv("AHS_TOOL_ROUNDS_RESULT")
	if output == "" {
		t.Skip("run through scripts/probe_tool_rounds.py")
	}
	var cases []*roundsResult
	for _, name := range []string{"ordinary_chat", "tool_inventory", "service_recovery", "task_continuation"} {
		f, access := trustedAgent(t)
		config := f.manager.agent.config()
		config.Enabled, config.TasksEnabled = true, true
		if err := f.manager.agent.save(config); err != nil {
			t.Fatal(err)
		}
		cfg := f.manager.Config().Companion
		cfg.Enabled, cfg.Permissions.ReplyText, cfg.Permissions.Remember = true, true, true
		cfg.QuietStart, cfg.QuietEnd = "00:00", "00:00"
		requests := map[string]string{"ordinary_chat": "周末聊聊吧。", "tool_inventory": "当前有哪些工具？", "service_recovery": "检查并恢复音乐桥接，再核对状态。"}
		requests["task_continuation"] = "继续原任务，根据已核实的进度保存下一步。"
		input := companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "rounds-current", Mode: "direct", Directed: true, Text: requests[name]}
		for i := 0; i < 6; i++ {
			past := input
			past.TraceID = fmt.Sprintf("rounds-history-%d", i)
			past.Text = "这是一条合成历史记录。"
			past.Source = &companion.SourceRef{MessageID: past.TraceID, Sender: past.Sender, Account: past.Account, Thread: past.Thread, Integrity: "verified"}
			if err := f.manager.memory.Observe(past, cfg, time.Now().Add(time.Duration(i-8)*time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		if name == "task_continuation" {
			input.TaskID, input.OriginTraceID = "rounds-original", "rounds-history-2"
			task := agentTask{ID: input.TaskID, Account: input.Account, Thread: input.Thread, Sender: input.Sender, OriginTrace: input.OriginTraceID, State: "waiting", Title: "合成任务", Checkpoint: "first_operation_verified", LastResult: "synthetic_existing_result"}
			if _, err := f.manager.agent.saveTask(task); err != nil {
				t.Fatal(err)
			}
		}
		access.SetTrace(input)
		catalog, err := access.List(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		host := &roundsSandbox{catalog: catalog}
		if name == "task_continuation" {
			host.taskCall = access.Call
		}
		r := &roundsResult{Scenario: name, Feedback: []string{}, Terminals: []string{}}
		model := &scriptedRounds{t: t, row: r, host: host}
		engine, err := companion.New(cfg, f.manager.memory, model, "合成角色")
		if err != nil {
			t.Fatal(err)
		}
		engine.Tools, engine.MaxToolCalls = host, 6
		ctx := companion.WithToolObserver(t.Context(), func(d companion.ToolDiagnostic) {
			switch d.Event {
			case "tool_completed", "tool_failed", "tool_blocked", "tool_cancelled":
				r.Terminals = append(r.Terminals, d.Tool+"="+d.Event)
				r.TerminalCount++
			}
		})
		decision, err := engine.Respond(ctx, input)
		if err != nil || decision.Action != "silent" || r.Outcome == "" {
			t.Fatalf("%s: outcome=%q decision=%q err=%v", name, r.Outcome, decision.Action, err)
		}
		r.ServiceActions = host.operations
		cases = append(cases, r)
		if name == "task_continuation" {
			saved := f.manager.agent.list(input.Account, input.Thread)
			if len(saved) != 1 || saved[0].ID != input.TaskID || saved[0].OriginTrace != input.OriginTraceID || saved[0].LastResult != "synthetic_existing_result" || saved[0].Checkpoint != "next_operation_pending" || len(host.operations) != 0 {
				t.Fatal("original task identity or existing result lost")
			}
		}
	}
	raw, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
