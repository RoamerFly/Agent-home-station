package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/mcptools"
)

type isolatedOverviewHost struct {
	catalog    []mcptools.Tool
	otherCalls int
}

func (h *isolatedOverviewHost) List(context.Context) ([]mcptools.Tool, error) { return h.catalog, nil }
func (h *isolatedOverviewHost) ToolOverview(context.Context) (map[string]any, error) {
	return map[string]any{"synthetic_music_bridge": map[string]any{"ready": true}}, nil
}
func (h *isolatedOverviewHost) Call(context.Context, string, json.RawMessage) (string, error) {
	h.otherCalls++
	return "", errors.New("isolated host does not execute external tools")
}

type boundedLiveOverviewModel struct {
	provider   *deepseek.Client
	requests   int
	plainCalls int
	actions    []string
	errors     []string
}

func (m *boundedLiveOverviewModel) Chat(context.Context, string, []map[string]string) (string, error) {
	m.plainCalls++
	return "", errors.New("native tool protocol required")
}
func (m *boundedLiveOverviewModel) ChatFunctions(ctx context.Context, system string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
	if m.requests >= 2 {
		return "", errors.New("isolated model decision budget exhausted")
	}
	m.requests++
	answer, err := m.provider.ChatFunctions(ctx, system, turns, tools)
	if err != nil {
		if d, ok := deepseek.ErrorDiagnostics(err); ok {
			m.errors = append(m.errors, d.Code)
		} else {
			m.errors = append(m.errors, "model_error")
		}
		return answer, err
	}
	var action struct{ Action, Tool string }
	if json.Unmarshal([]byte(answer), &action) == nil {
		m.actions = append(m.actions, action.Action+"/"+action.Tool)
	} else {
		if len(answer) > 0 && answer[0] != '{' && answer[0] != '[' && answer[0] != '`' {
			m.actions = append(m.actions, "plain_text")
		} else {
			m.actions = append(m.actions, "json_like_invalid")
		}
	}
	return answer, nil
}

func TestIsolatedLiveToolOverview(t *testing.T) {
	configDir := os.Getenv("AHS_LIVE_OVERVIEW_CONFIG")
	if configDir == "" {
		t.Skip("explicit isolated live provider acceptance")
	}
	live, err := loadConfig(configDir)
	if err != nil {
		t.Fatal("configured provider settings unavailable")
	}
	f, access := trustedAgent(t)
	agentCfg := f.manager.agent.config()
	agentCfg.Enabled = true
	if err := f.manager.agent.save(agentCfg); err != nil {
		t.Fatal(err)
	}
	input := companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "isolated-live-tool-overview", Source: &companion.SourceRef{InstanceID: "isolated-overview", MessageID: "isolated-live-tool-overview", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", Integrity: "verified"}, Mode: "direct", Directed: true, Text: "检查一下你目前有哪些可用工具，按类别和数量简洁回答。"}
	access.SetTrace(input)
	catalog, err := access.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	host := &isolatedOverviewHost{catalog: catalog}
	provider, err := f.manager.makeModel(live)
	if err != nil {
		t.Fatal("configured provider unavailable")
	}
	model := &boundedLiveOverviewModel{provider: provider}
	cfg := f.manager.Config().Companion
	cfg.Enabled, cfg.Permissions.ReplyText, cfg.Permissions.Remember = true, true, true
	engine, err := companion.New(cfg, f.manager.memory, model, live.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools, engine.MaxToolCalls = host, 1
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	terminals := []string{}
	ctx = companion.WithToolObserver(ctx, func(d companion.ToolDiagnostic) {
		if d.Event == "tool_completed" || d.Event == "tool_failed" || d.Event == "tool_cancelled" {
			terminals = append(terminals, d.Tool+"="+d.Event)
		}
	})
	providerAttempts, providerReported, inputTokens, outputTokens := 0, 0, int64(0), int64(0)
	ctx = deepseek.WithUsageObserver(ctx, func(u deepseek.Usage) {
		providerAttempts++
		if u.Reported {
			providerReported++
			inputTokens += u.Input
			outputTokens += u.Output
		}
	})
	decision, runErr := engine.Respond(ctx, input)
	result := map[string]any{"model_decisions": model.requests, "plain_review_calls": model.plainCalls, "model_actions": model.actions, "model_error_codes": model.errors, "provider_attempts": providerAttempts, "provider_reported": providerReported, "input_tokens": inputTokens, "output_tokens": outputTokens, "tool_terminals": terminals, "external_tool_calls": host.otherCalls, "decision_action": decision.Action, "decision_reason": decision.Reason, "succeeded": runErr == nil}
	if path := os.Getenv("AHS_LIVE_OVERVIEW_RESULT"); path != "" {
		if raw, e := json.MarshalIndent(result, "", "  "); e == nil {
			if e = os.WriteFile(path, raw, 0600); e != nil {
				t.Fatal(e)
			}
		}
	}
	t.Logf("isolated overview: %+v", result)
	if runErr != nil || model.requests != 2 || host.otherCalls != 0 || len(terminals) != 1 || terminals[0] != "tool_overview=tool_completed" || decision.Action != "reply" {
		t.Fatal("isolated real model did not complete the one-tool overview path")
	}
}

type localOverviewPlainModel struct{ calls int }

func (*localOverviewPlainModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", errors.New("unexpected plain model call")
}
func (m *localOverviewPlainModel) ChatFunctions(context.Context, string, []map[string]string, []mcptools.Tool) (string, error) {
	m.calls++
	if m.calls == 1 {
		return `{"action":"tool","tool":"tool_overview","arguments":{}}`, nil
	}
	return "当前工具按类别已汇总。", nil
}

func TestIsolatedOverviewPlainReply(t *testing.T) {
	f, access := trustedAgent(t)
	agentCfg := f.manager.agent.config()
	agentCfg.Enabled = true
	if err := f.manager.agent.save(agentCfg); err != nil {
		t.Fatal(err)
	}
	input := companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "isolated-live-tool-overview", Source: &companion.SourceRef{InstanceID: "isolated-overview", MessageID: "isolated-live-tool-overview", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", Integrity: "verified"}, Mode: "direct", Directed: true, Text: "检查一下你目前有哪些可用工具，按类别和数量简洁回答。"}
	access.SetTrace(input)
	catalog, err := access.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg := f.manager.Config().Companion
	cfg.Enabled, cfg.Permissions.ReplyText, cfg.Permissions.Remember = true, true, true
	model := &localOverviewPlainModel{}
	engine, err := companion.New(cfg, f.manager.memory, model, "合成角色")
	if err != nil {
		t.Fatal(err)
	}
	host := &isolatedOverviewHost{catalog: catalog}
	engine.Tools, engine.MaxToolCalls = host, 1
	d, err := engine.Respond(t.Context(), input)
	if failure, ok := companion.ReviewFailureFromError(err); ok {
		t.Logf("local review failure: stage=%s code=%s cause=%v", failure.Stage, failure.Code, errors.Unwrap(err))
	}
	if err != nil || model.calls != 2 || host.otherCalls != 0 || d.Action != "reply" {
		t.Fatalf("local overview path: calls=%d other=%d action=%s err=%v", model.calls, host.otherCalls, d.Action, err)
	}
}
