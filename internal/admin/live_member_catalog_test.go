package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

type readOnlyMemberTools struct {
	p     *permittedTools
	calls []json.RawMessage
}

func (r *readOnlyMemberTools) List(ctx context.Context) ([]mcptools.Tool, error) {
	all, _ := r.p.List(ctx)
	out := []mcptools.Tool{}
	for _, t := range all {
		if t.Name == "wechat_read" {
			out = append(out, t)
		}
	}
	return out, nil
}
func (r *readOnlyMemberTools) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if name != "wechat_read" {
		return "", &linuxnative.CatalogError{Code: "catalog_kind_invalid"}
	}
	r.calls = append(r.calls, append(json.RawMessage(nil), args...))
	return r.p.Call(ctx, name, args)
}

func TestLiveMemberReadThroughAgentToolWithoutSending(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_MEMBER_READ_LIVE") != "1" {
		t.Skip("explicit read-only current group diagnosis")
	}
	f := setup(t)
	cfg, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal("config unavailable")
	}
	f.manager.config = cfg
	// Copy only settings into the isolated manager; opening live task storage
	// through the startup constructor would run recovery on another worker.
	settings, err := os.ReadFile(filepath.Join(root, "admin/agent-config.json"))
	if err != nil {
		t.Fatal("agent settings unavailable")
	}
	if json.Unmarshal(settings, &f.manager.agent.settings) != nil {
		t.Fatal("agent settings invalid")
	}
	native, err := linuxnative.NewAdmin(linuxnative.Config{URL: "http://127.0.0.1:6174", TokenFile: filepath.Join(root, "api-token"), Account: "background-test-20261024"})
	if err != nil {
		t.Fatal(err)
	}
	f.manager.native = native
	p, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	defer p.hub.Close()
	thread := ""
	for _, rule := range cfg.GroupRules {
		if rule.Name == "机器人测试群" {
			thread = rule.ID
			break
		}
	}
	if thread == "" {
		t.Fatal("configured test group not found")
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: native.Account(), Thread: thread, Sender: "member-diagnostic-user", TraceID: "read-only-member-diagnostic"})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := p.Call(ctx, "wechat_read", json.RawMessage(`{"kind":"members"}`))
	if err != nil {
		t.Fatal("actual agent tool", err)
	}
	var page linuxnative.CatalogPage
	if json.Unmarshal([]byte(result), &page) != nil || len(page.Items) == 0 {
		t.Fatal("no real members")
	}
	for _, row := range page.Items {
		if row["id"] == nil || row["displayName"] == nil {
			t.Fatal("missing member fields")
		}
	}
	if workspace != "" {
		b, _ := json.MarshalIndent(map[string]any{"actual_agent_tool": true, "member_count": len(page.Items), "wechat_sends": 0}, "", "  ")
		os.WriteFile(filepath.Join(workspace, "research/evidence/member-tool-live.json"), b, 0600)
	}
	t.Log("Actual agent tool returned", len(page.Items), "members; no sends")
	if os.Getenv("WECHAT_MEMBER_MODEL_LIVE") == "1" {
		provider, err := f.manager.makeModel(cfg)
		if err != nil {
			t.Fatal("provider unavailable")
		}
		c := cfg.Companion
		c.Enabled = true
		c.Permissions.Remember = false
		engine, err := companion.New(c, f.manager.memory, provider, cfg.SystemPrompt)
		if err != nil {
			t.Fatal(err)
		}
		tools := &readOnlyMemberTools{p: p}
		engine.Tools = tools
		engine.MaxToolCalls = 4
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		d, err := engine.Respond(ctx, companion.Input{Channel: "wechat", Account: native.Account(), Thread: p.trace.Thread, Sender: p.trace.Sender, TraceID: "member-query-isolated", Mode: "direct", Directed: true, IsGroup: true, Text: "你知道我们群的各群成员的群名称吗？请先实际读取群成员资料再回答。"})
		if err != nil || d.Action != "reply" || len(tools.calls) == 0 {
			t.Fatal("real model member read failed", err, d.Action, tools.calls)
		}
		t.Log("Real model selected member lookup and produced a reply; no WeChat sends")
	}
}
