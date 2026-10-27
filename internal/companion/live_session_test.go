package companion

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/agentrun"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/mcptools"
)

// Opt-in provider integration only: synthetic memory, synthetic observations,
// no real filesystem tools, no WeChat transport and no production journal.
type liveSessionTools struct {
	session *agentrun.Session
	calls   int
}

func (t *liveSessionTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "workspace_read", Description: "Read synthetic fixture.md. No access to actual files.", InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`)}}, nil
}
func (t *liveSessionTools) Call(_ context.Context, name string, args json.RawMessage) (string, error) {
	t.calls++
	if name != "workspace_read" || t.calls > 1 {
		return "", toolProbeError{}
	}
	t.session.Offer("fixture-account", agentrun.Event{ID: "fixture-followup", Thread: "fixture-thread", Sender: "fixture-person", Text: "改一下，不要再读取文件了，只回复这四个字：海盐汽水"})
	return `{"document":"fixture.md","text":"测试文档：颜色是蓝色。","fixture_only":true}`, nil
}

type toolProbeError struct{}

func (toolProbeError) Error() string { return "unexpected synthetic tool invocation" }

func TestLiveModelSessionFollowup(t *testing.T) {
	path := os.Getenv("WECHAT_AGENT_LIVE_SESSION_CONFIG")
	if path == "" {
		t.Skip("set WECHAT_AGENT_LIVE_SESSION_CONFIG to opt into a billed synthetic provider test")
	}
	var config struct {
		BaseURL   string `json:"base_url"`
		Model     string `json:"model"`
		Provider  string `json:"provider"`
		Companion Config `json:"companion"`
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &config) != nil {
		t.Fatal("private test configuration unavailable")
	}
	model, err := deepseek.New(deepseek.Config{BaseURL: config.BaseURL, Model: config.Model, KeyFile: filepath.Join(filepath.Dir(path), "provider-key"), MaxTokens: 768, ToolMaxTokens: 2048, Compatible: config.Provider == "openai-compatible"})
	if err != nil {
		t.Fatal(err)
	}
	c := config.Companion
	c.Enabled = true
	c.ShowAILabel = false
	c.Permissions.Remember = false
	s := agentrun.New(agentrun.Scope{Account: "fixture-account", Thread: "fixture-thread", Sender: "fixture-person", RunID: "fixture-initial"}, nil, nil)
	tools := &liveSessionTools{session: s}
	e, err := NewPreview(c, model, "隔离功能验证，只有合成消息与只读假工具，不存在真实微信发送能力。遵循当前用户最新要求，最终输出reply/silent JSON。")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = tools
	e.MaxToolCalls = 3
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := e.Respond(agentrun.WithSession(ctx, s), Input{Channel: "wechat", Account: "fixture-account", Thread: "fixture-thread", Sender: "fixture-person", TraceID: "fixture-initial", Mode: "direct", Text: "请用workspace_read读取fixture.md，再告诉我文档中的颜色。", Preview: true})
	if err != nil || tools.calls != 1 || !s.Consumed("fixture-followup") || d.Action != "reply" || strings.Join(d.Messages, "") != "海盐汽水" {
		t.Fatal("live model did not follow new message", d, err, tools.calls)
	}
	t.Log("Real provider: one synthetic tool call, latest followup honored; zero real WeChat sends and zero real conversation reads.")
}
