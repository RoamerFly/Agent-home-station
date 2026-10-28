package admin

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestDeliveryWorkingRuleMergesAndSurvivesRestart(t *testing.T) {
	f := setup(t)
	d, e := f.manager.workspace.Read("shared", "AGENTS.md")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.manager.workspace.Write("shared", "AGENTS.md", "# 私有备注\n保留主人设定\n", d.Revision); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e = f.manager.initializeWorkspace(); e != nil {
			t.Fatal(e)
		}
	}
	d, e = f.manager.workspace.Read("shared", "AGENTS.md")
	if e != nil || !strings.Contains(d.Content, "保留主人设定") || strings.Count(d.Content, "## 投递先核查，续办原批次") != 1 {
		t.Fatal(d, e)
	}
	if strings.Count(d.Content, "## 自然表达，直接回应") != 1 {
		t.Fatal("natural style note missing or duplicated", d)
	}
}

func TestAutonomousMediaRepairReportIsPrivateAndDoesNotGrantOwnerIdentity(t *testing.T) {
	f, p := trustedAgent(t)
	in := p.trace
	in.Thread = "source@chatroom"
	in.Sender = "ordinary-member"
	in.TraceID = "surf-observation"
	in.Surf = true
	for i := 0; i < 2; i++ {
		if e := f.manager.queueMediaRepair(in, "native-job", "media_processing_failed"); e != nil {
			t.Fatal(e)
		}
	}
	found := 0
	for _, task := range f.manager.agent.list(in.Account, "") {
		if task.DeliveryJob != "native-job" {
			continue
		}
		found++
		if task.Thread == in.Thread || !task.ReportOnly || task.OwnerRequested || task.Sender != "" || task.ReportSourceThread != in.Thread {
			t.Fatal("source member inherited owner privileges", task)
		}
	}
	if found != 1 {
		t.Fatal("duplicate repair tasks", found)
	}
}

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

func TestSearchGuideMigratesLegacyPaginationWithoutReplacingPersonalNotes(t *testing.T) {
	f := setup(t)
	d, e := f.manager.workspace.Read("shared", "tool-reading.md")
	if e != nil {
		t.Fatal(e)
	}
	old := "# 自定义说明\n保留这段项目备注。\nserver_list、server_search、workspace_list/search通过next_offset继续\n"
	if _, e = f.manager.workspace.Write("shared", "tool-reading.md", old, d.Revision); e != nil {
		t.Fatal(e)
	}
	if e = f.manager.initializeWorkspace(); e != nil {
		t.Fatal(e)
	}
	d, e = f.manager.workspace.Read("shared", "tool-reading.md")
	if e != nil || !strings.Contains(d.Content, "保留这段项目备注。") || !strings.Contains(d.Content, "server_search使用next_cursor") || strings.Contains(d.Content, "server_list、server_search、workspace_list/search") {
		t.Fatal(d, e)
	}
	guide, e := f.manager.workspace.Read("shared", "project-search.md")
	if e != nil || !strings.Contains(guide.Content, "不是整个任务的上限") {
		t.Fatal(guide, e)
	}
}

func TestMediaExpressionGuideMigratesOnceAndPreservesCustomNotes(t *testing.T) {
	f := setup(t)
	d, err := f.manager.workspace.Read("shared", "AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.manager.workspace.Write("shared", "AGENTS.md", "# 主人备注\n保留这段私人设定。\n", d.Revision); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = f.manager.initializeWorkspace(); err != nil {
			t.Fatal(err)
		}
	}
	d, err = f.manager.workspace.Read("shared", "AGENTS.md")
	if err != nil || strings.Count(d.Content, "## 角色演唱与视频内容") != 1 || !strings.Contains(d.Content, "保留这段私人设定。") {
		t.Fatal(d, err)
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
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux system-isolated executor; Windows LPAC is validated separately")
	}
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

func TestDiagnosticProcessResultReturnsToParentInsteadOfReopeningUserRequest(t *testing.T) {
	f, p := trustedAgent(t)
	cfg := f.manager.agent.config()
	cfg.Host.Mode = "full"
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	parent := agentTask{ID: "business-parent", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", State: "waiting", RequestedText: "用RVC直接翻唱合成测试曲", Checkpoint: "检查本次任务状态"}
	f.manager.agent.task(parent)
	trace := p.trace
	trace.TaskID = parent.ID
	trace.RequestedText = parent.RequestedText
	p.SetTrace(trace)
	result, err := p.Call(context.Background(), "server_process", json.RawMessage(`{"action":"start","path":"`+strings.ReplaceAll(cfg.Host.Roots[0], `\`, `\\`)+`","command":"echo synthetic-diagnostic-result","request_key":"diagnostic"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		ID     string `json:"id"`
		TaskID string `json:"task_id"`
	}
	json.Unmarshal([]byte(result), &out)
	child, ok := f.manager.agent.lookupTask(out.TaskID, parent.Account, parent.Thread)
	if !ok || child.ParentID != parent.ID || child.RequestedText != "" {
		t.Fatal("diagnostic duplicated old user request", child)
	}
	for i := 0; i < 400; i++ {
		status, err := f.manager.processes.Status(agentID(parent.Account, parent.Thread, parent.Sender), out.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		if status.State != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	input, err := f.manager.claimProcessTask(child, time.Now())
	if err != nil || input != nil {
		t.Fatal("child started a separate model reply", input, err)
	}
	updated, _ := f.manager.agent.lookupTask(parent.ID, parent.Account, parent.Thread)
	if len(updated.Actions) < 1 || updated.Actions[len(updated.Actions)-1].Tool != "background_process_result" {
		t.Fatal("parent did not receive verified result", updated)
	}
	if len(f.manager.agent.active(parent.Account, parent.Thread)) != 1 || len(f.manager.agent.taskHistory(parent.Account, parent.Thread)) != 1 {
		t.Fatal("diagnostic watcher did not leave active queue")
	}
}
