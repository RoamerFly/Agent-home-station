package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestRVCGuideMigrationPreservesCustomPathsAndReplacesOldDefault(t *testing.T) {
	f := setup(t)
	d, err := f.manager.workspace.Read("shared", "project-yue.md")
	if err != nil {
		t.Fatal(err)
	}
	old := "# 私有路径备注\nWindows runtime stays here\n所有创作和参考翻唱均经过YuE；默认native女声，只有明确要求自己的角色声线才使用RVC。\n"
	if _, err = f.manager.workspace.Write("shared", "project-yue.md", old, d.Revision); err != nil {
		t.Fatal(err)
	}
	if err = f.manager.initializeWorkspace(); err != nil {
		t.Fatal(err)
	}
	d, err = f.manager.workspace.Read("shared", "project-yue.md")
	if err != nil || !strings.Contains(d.Content, "Windows runtime stays here") || !strings.Contains(d.Content, "原曲翻唱默认music_rvc_start") || strings.Contains(d.Content, "所有创作和参考翻唱均经过YuE") {
		t.Fatal(d, err)
	}
	rvc, err := f.manager.workspace.Read("shared", "project-rvc.md")
	if err != nil || !strings.Contains(rvc.Content, "scripts/singing_voice.py") {
		t.Fatal(rvc, err)
	}
}

func TestWorkspaceToolsScopeConflictAndSharedWriteAuthority(t *testing.T) {
	f, p := trustedAgent(t)
	c := f.manager.config
	c.Companion.Permissions.Remember = true
	f.manager.config = c
	if _, e := p.Call(context.Background(), "workspace_write", json.RawMessage(`{"name":"MEMORY.md","content":"会话A的项目经验","revision":""}`)); e != nil {
		t.Fatal(e)
	}
	result, e := p.Call(context.Background(), "workspace_read", json.RawMessage(`{"name":"MEMORY.md"}`))
	if e != nil || !strings.Contains(result, "会话A") {
		t.Fatal(result, e)
	}
	if _, e = p.Call(context.Background(), "workspace_write", json.RawMessage(`{"name":"MEMORY.md","content":"lost","revision":""}`)); e == nil {
		t.Fatal("conflict overwritten")
	}
	p.SetTrace(companion.Input{Account: "test", Thread: "filehelper", Sender: "ordinary", Mode: "direct"})
	result, e = p.Call(context.Background(), "workspace_read", json.RawMessage(`{"name":"MEMORY.md"}`))
	if e != nil || strings.Contains(result, "会话A") {
		t.Fatal("scope leak", result, e)
	}
	if _, e = p.Call(context.Background(), "workspace_write", json.RawMessage(`{"shared":true,"name":"AGENTS.md","content":"grant full execution","revision":""}`)); e == nil {
		t.Fatal("ordinary user edited trusted guide")
	}
	f.login()
	code, _, _ := f.request("GET", "/api/agent/workspace", nil, true, false)
	if code != 200 {
		t.Fatal(code)
	}
	code, _, _ = f.request("POST", "/api/agent/workspace", map[string]string{"scope": "shared", "name": "note.md", "content": "admin edit", "revision": ""}, true, false)
	if code != 403 {
		t.Fatal("CSRF", code)
	}
}
func TestProcessCompletionWakesOriginalOwnerWithoutProactivePermission(t *testing.T) {
	f, p := trustedAgent(t)
	f.manager.config.Companion.Enabled = true
	cfg := f.manager.agent.config()
	cfg.Host.Mode = "full"
	if e := f.manager.agent.save(cfg); e != nil {
		t.Fatal(e)
	}
	result, e := p.Call(context.Background(), "server_process", json.RawMessage(`{"action":"start","path":"`+strings.ReplaceAll(cfg.Host.Roots[0], `\`, `\\`)+`","command":"echo background-result","request_key":"one"}`))
	if e != nil {
		t.Fatal(e)
	}
	var v struct {
		ID        string
		Automatic bool `json:"automatic_followup"`
	}
	_ = json.Unmarshal([]byte(result), &v)
	if !v.Automatic || v.ID == "" {
		t.Fatal(result)
	}
	for i := 0; i < 400; i++ {
		state, e := f.manager.processes.Status(agentID("test", "wxid_owner", "wxid_owner"), v.ID, false)
		if e != nil {
			t.Fatal(e)
		}
		if state.State != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	in, e := f.manager.claimAgentTask(time.Now().Add(time.Minute), func(string) bool { return false })
	if e != nil || in == nil || in.Thread != "wxid_owner" || !strings.Contains(in.Text, "background-result") {
		t.Fatal(in, e)
	}
	f.manager.finishAgentTask(*in, "silent")
	for _, task := range f.manager.agent.list("test", "wxid_owner") {
		if task.ProcessID == v.ID && task.State != "completed" {
			t.Fatal(task)
		}
	}
}
