package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/mcptools"
)

type projectReadProbe struct {
	p           *permittedTools
	calls       map[string]int
	projectRoot string
}

func (r *projectReadProbe) SetTrace(in companion.Input)             { r.p.SetTrace(in) }
func (r *projectReadProbe) AgentContext(ctx context.Context) string { return r.p.AgentContext(ctx) }
func (r *projectReadProbe) List(ctx context.Context) ([]mcptools.Tool, error) {
	all, err := r.p.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []mcptools.Tool
	for _, tool := range all {
		if tool.Name == "workspace_list" || tool.Name == "workspace_read" || tool.Name == "server_status" || tool.Name == "server_read" || tool.Name == "music_service_status" {
			out = append(out, tool)
		}
	}
	return out, nil
}
func (r *projectReadProbe) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "workspace_list", "workspace_read", "server_status", "music_service_status":
	case "server_read":
		var in struct{ Path string }
		if json.Unmarshal(args, &in) != nil || in.Path != filepath.Join(r.projectRoot, "scripts/Start-Local-YuE.ps1") && in.Path != filepath.Join(r.projectRoot, "components/yue2/app/README.md") {
			return "", errors.New("probe permits only project README or startup script")
		}
	default:
		return "", errors.New("read-only probe; no writes, programs, generation or WeChat sends")
	}
	r.calls[name]++
	return r.p.Call(ctx, name, args)
}
func TestLiveOwnerReadsRealProjectGuidesAndReportsServiceHealth(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || workspace == "" || os.Getenv("WECHAT_PROJECT_ACCESS_LIVE") != "1" {
		t.Skip("explicit real-model read-only project access test")
	}
	live, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f, p := trustedAgent(t)
	settings := f.manager.agent.config()
	settings.Host.Mode = "read"
	settings.Host.Roots = []string{workspace}
	if err = f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	speech, err := loadSpeech(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	writeLegacySpeechMigrationFixture(t, f.manager, speech)
	guide, err := os.ReadFile(filepath.Join(root, "admin/agent-workspace/shared/project-yue.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := f.manager.workspace.Read("shared", "project-yue.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.manager.workspace.Write("shared", "project-yue.md", string(guide), doc.Revision); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(f.dir, "live-key")
	if err = os.WriteFile(key, []byte(live.APIKey), 0600); err != nil {
		t.Fatal(err)
	}
	model, err := deepseek.New(deepseek.Config{BaseURL: live.BaseURL, Model: live.Model, KeyFile: key, Compatible: live.Provider == "openai-compatible", MaxTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	c := f.manager.config.Companion
	c.Enabled = true
	c.Permissions.Remember = true
	engine, err := companion.New(c, f.manager.memory, model, live.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	probe := &projectReadProbe{p: p, calls: map[string]int{}, projectRoot: workspace}
	engine.Tools = probe
	engine.MaxToolCalls = agentTurnToolLimit
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	decision, err := engine.Respond(ctx, companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "project-access-readonly", Mode: "direct", Directed: true, Text: "检查YuE是否恢复：先读共享项目指南，并实际读取相关项目README或启动脚本，确认程序在哪个系统节点运行，再核对当前服务状态。告诉我你实际检查了什么和验证结论。不要提交歌曲、修改文件、执行程序或发送微信消息。项目根目录是 " + workspace + "。"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != "reply" || probe.calls["workspace_read"] == 0 || probe.calls["server_read"] == 0 || probe.calls["music_service_status"] == 0 {
		t.Fatal("model did not read and report", decision.Action, probe.calls)
	}
	if !strings.Contains(strings.ToLower(strings.Join(decision.Messages, " ")), "windows") {
		t.Fatal("report did not identify actual OS")
	}
	if path := os.Getenv("WECHAT_PROJECT_ACCESS_REPORT"); path != "" {
		report, _ := json.MarshalIndent(map[string]any{"real_model": true, "real_project_document_read": true, "real_service_health_checked": true, "tool_calls": probe.calls, "decision": decision.Action, "messages": decision.Messages, "files_changed": 0, "songs_created": 0, "wechat_sends": 0}, "", "  ")
		if err = os.WriteFile(path, report, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
