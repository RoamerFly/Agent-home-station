package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func trustedAgent(t *testing.T) (*webFixture, *permittedTools) {
	t.Helper()
	f := setup(t)
	f.manager.mu.Lock()
	f.manager.config.PrivateEnabled = true
	f.manager.config.DirectIDs = []string{"wxid_owner"}
	f.manager.mu.Unlock()
	c := f.manager.agent.config()
	c.OwnerIDs = []string{"wxid_owner"}
	c.PrivilegedThreads = []string{"wxid_owner"}
	c.Host.Mode = "files"
	c.Host.Roots = []string{t.TempDir()}
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	p, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "turn-1", Mode: "direct"})
	return f, p
}
func TestAgentPrivilegeChecksAtDiscoveryAndExecution(t *testing.T) {
	_, p := trustedAgent(t)
	if !p.agentToolEnabled("server_write") {
		t.Fatal("owner lacks tool")
	}
	b, _ := json.Marshal(map[string]string{"path": filepath.Join(p.manager.agent.config().Host.Roots[0], "owner.txt"), "content": "ok"})
	for _, in := range []companion.Input{{Account: "test", Thread: "wxid_owner", Sender: "wxid_attacker"}, {Account: "test", Thread: "filehelper", Sender: "wxid_owner"}, {Account: "other", Thread: "wxid_owner", Sender: "wxid_owner"}, {Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", Preview: true}, {Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", Surf: true}} {
		p.SetTrace(in)
		if p.agentToolEnabled("server_write") {
			t.Fatal("untrusted context offered host")
		}
		if _, err := p.Call(context.Background(), "server_write", b); err == nil {
			t.Fatal("untrusted context called host")
		}
	}
}

func TestOwnerAllAllowedThreadsStillChecksSenderAndRecipient(t *testing.T) {
	f, p := trustedAgent(t)
	c := f.manager.agent.config()
	c.PrivilegedThreads = []string{"*"}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	// A newly allowed group does not need a second privileged-thread entry.
	f.manager.mu.Lock()
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "new_group@chatroom", Mode: "mention"})
	f.manager.mu.Unlock()
	for _, thread := range []string{"wxid_owner", "new_group@chatroom"} {
		p.SetTrace(companion.Input{Account: "test", Thread: thread, Sender: "wxid_owner"})
		if !p.agentToolEnabled("server_write") {
			t.Fatal("owner denied in allowed conversation", thread)
		}
	}
	args, _ := json.Marshal(map[string]string{"path": filepath.Join(c.Host.Roots[0], "must-not-exist.txt"), "content": "forbidden"})
	for _, input := range []companion.Input{
		{Account: "test", Thread: "new_group@chatroom", Sender: "wxid_imposter"},
		{Account: "test", Thread: "not_allowed@chatroom", Sender: "wxid_owner"},
		{Account: "test", Sender: "wxid_owner"},
		{Account: "other", Thread: "new_group@chatroom", Sender: "wxid_owner"},
		{Account: "test", Thread: "new_group@chatroom", Sender: "wxid_owner", Preview: true},
		{Account: "test", Thread: "new_group@chatroom", Sender: "wxid_owner", Surf: true},
	} {
		p.SetTrace(input)
		if p.agentToolEnabled("server_write") {
			t.Fatalf("untrusted context offered host: %+v", input)
		}
		if _, err := p.Call(context.Background(), "server_write", args); err == nil {
			t.Fatalf("untrusted context executed host: %+v", input)
		}
	}
	if _, err := os.Stat(filepath.Join(c.Host.Roots[0], "must-not-exist.txt")); !os.IsNotExist(err) {
		t.Fatal("unauthorized write reached filesystem", err)
	}
	for _, mutate := range []func(*agentSettings){
		func(s *agentSettings) { s.OwnerIDs = []string{"*"} },
		func(s *agentSettings) { s.OwnerIDs = nil },
		func(s *agentSettings) { s.PrivilegedThreads = []string{"*", "wxid_owner"} },
	} {
		invalid := c
		mutate(&invalid)
		if invalid.validate() == nil {
			t.Fatal("invalid wildcard authorization accepted")
		}
	}
}

func TestExternalPluginOwnerRestrictionCannotBeBypassed(t *testing.T) {
	f, p := trustedAgent(t)
	c := f.manager.agent.config()
	c.ExternalOwnerOnly = true
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	p.apiNames = map[string]bool{"voice_provider": true}
	if !p.enabled("voice_provider") || !p.enabled("external__tool") {
		t.Fatal("trusted plugin denied")
	}
	p.trace.Sender = "wxid_someone_else"
	if p.enabled("voice_provider") || p.enabled("external__tool") {
		t.Fatal("plugin bypassed owner restriction")
	}
	if !p.enabled("web_fetch") {
		t.Fatal("public web tool unnecessarily blocked")
	}
}
func TestAgentHostOperationDeduplicatedAcrossRestart(t *testing.T) {
	f, p := trustedAgent(t)
	path := filepath.Join(f.manager.agent.config().Host.Roots[0], "once.txt")
	b, _ := json.Marshal(map[string]string{"path": path, "content": "once"})
	first, err := p.Call(context.Background(), "server_write", b)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Call(context.Background(), "server_write", b)
	if err != nil || first != second {
		t.Fatal("operation replay", first, second, err)
	}
	next, err := newAgentControl(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.agent = next
	third, err := p.Call(context.Background(), "server_write", b)
	if err != nil || first != third {
		t.Fatal("restart replay", third, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "once" {
		t.Fatal("bad file")
	}
	// A claimed operation with no result must remain unknown, never executed.
	id := agentID(p.trace.Account, p.trace.Thread, p.trace.TraceID, "server_write", string(b))
	next.mu.Lock()
	op := next.data.Operations[id]
	op.Result = `{"state":"unknown"}`
	op.State = "running"
	next.data.Operations[id] = op
	next.persist()
	next.mu.Unlock()
	result, err := p.Call(context.Background(), "server_write", b)
	if err != nil || !strings.Contains(result, "unknown") {
		t.Fatal(result, err)
	}
}
func TestAgentSettingsAPIAuthenticationAndOptimisticSave(t *testing.T) {
	f := setup(t)
	if code, _, _ := f.request("GET", "/api/agent", nil, false, false); code != 401 {
		t.Fatal(code)
	}
	f.login()
	c := f.manager.agent.config()
	if code, _, _ := f.request("POST", "/api/agent/config", c, true, false); code != 403 {
		t.Fatal(code)
	}
	if code, _, _ := f.request("POST", "/api/agent/config", c, true, true); code != 200 {
		t.Fatal(code)
	}
	if code, _, _ := f.request("POST", "/api/agent/config", c, true, true); code != 409 {
		t.Fatal("stale configuration overwritten", code)
	}
	c = f.manager.agent.config()
	c.Host.Mode = "full"
	if code, _, _ := f.request("POST", "/api/agent/config", c, true, true); code != 400 {
		t.Fatal("unbound full mode", code)
	}
}

func TestAgentCrossSendReceiptInspectionAndMemory(t *testing.T) {
	f, p := trustedAgent(t)
	native := &fakeTools{fakeNative: f.native}
	f.manager.native = native
	c := f.manager.agent.config()
	c.CrossConversation = true
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	f.manager.mu.Lock()
	f.manager.config.Behavior.RandomDelay = false
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.mu.Unlock()
	args := json.RawMessage(`{"thread_id":"filehelper","text":"来自已授权测试的消息","request_key":"one-send"}`)
	result, err := p.Call(context.Background(), "wechat_send_text", args)
	if err != nil {
		t.Fatal(err)
	}
	var submitted struct {
		OperationID string `json:"operation_id"`
		State       string `json:"state"`
	}
	if json.Unmarshal([]byte(result), &submitted) != nil || submitted.OperationID == "" || submitted.State != "queued" {
		t.Fatal(result)
	}
	if _, err = p.Call(context.Background(), "wechat_send_text", args); err != nil || native.submits.Load() != 1 {
		t.Fatal("send replayed", err, native.submits.Load())
	}
	if _, err = p.Call(context.Background(), "wechat_send_text", json.RawMessage(`{"thread_id":"filehelper","text":"偷偷改成新内容","request_key":"one-send"}`)); err == nil || native.submits.Load() != 1 {
		t.Fatal("logical send key reused with changed body")
	}
	statusArgs, _ := json.Marshal(map[string]string{"operation_id": submitted.OperationID})
	status, err := p.Call(context.Background(), "wechat_operation_status", statusArgs)
	if err != nil || !strings.Contains(status, `"state":"accepted"`) {
		t.Fatal(status, err)
	}
	scope := f.manager.memory.Context(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper"}, f.manager.Config().Companion, time.Now())
	if len(scope.Messages) != 1 || scope.Messages[0].Role != "assistant" || scope.Messages[0].Text != "来自已授权测试的消息" {
		t.Fatal("accepted tool send absent from memory", scope)
	}
	if _, err = p.Call(context.Background(), "wechat_operation_status", statusArgs); err != nil {
		t.Fatal(err)
	}
	scope = f.manager.memory.Context(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper"}, f.manager.Config().Companion, time.Now())
	if len(scope.Messages) != 1 || native.submits.Load() != 1 {
		t.Fatal("receipt poll replayed send or duplicated memory")
	}
}
func TestAgentTaskScopedPersistenceRecoveryAndQuietBudget(t *testing.T) {
	f, p := trustedAgent(t)
	in := `{"key":"music","title":"找一首歌","checkpoint":"已经找到来源，稍后检查","state":"waiting","delay_seconds":30}`
	if _, err := p.Call(context.Background(), "agent_task", json.RawMessage(in)); err != nil {
		t.Fatal(err)
	}
	tasks := f.manager.agent.list("test", "wxid_owner")
	if len(tasks) != 1 {
		t.Fatal(tasks)
	}
	old := p.trace
	p.trace.Sender = "wxid_other"
	if _, err := p.Call(context.Background(), "agent_task", json.RawMessage(`{"id":"`+tasks[0].ID+`","checkpoint":"hijack","state":"cancelled"}`)); err == nil {
		t.Fatal("task hijacked")
	}
	p.trace = old
	if len(f.manager.agent.list("test", "elsewhere")) != 0 {
		t.Fatal("scope escaped")
	}
	task := tasks[0]
	task.State = "running"
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	recovered, err := newAgentControl(f.dir)
	if err != nil || recovered.list("test", "")[0].State != "needs_review" {
		t.Fatal("running effect automatically resumed", err)
	}
	cfg := f.manager.Config()
	cfg.Companion.Enabled = true
	cfg.Companion.Permissions.Remember = true
	cfg.Companion.Permissions.ReplyText = true
	cfg.Companion.Permissions.Proactive = true
	cfg.Companion.ProactiveThreads = []string{"wxid_owner"}
	now := time.Now()
	zone, _ := time.LoadLocation(cfg.Companion.Timezone)
	local := now.In(zone)
	cfg.Companion.QuietStart = local.Add(-time.Hour).Format("15:04")
	cfg.Companion.QuietEnd = local.Add(time.Hour).Format("15:04")
	f.manager.mu.Lock()
	f.manager.config = cfg.Config
	f.manager.mu.Unlock()
	task.State = "waiting"
	task.Next = now.Add(-time.Minute)
	f.manager.agent.task(task)
	if result, err := f.manager.claimAgentTask(now, func(string) bool { return false }); err != nil || result != nil {
		t.Fatal("quiet task ran", result, err)
	}
	_, count := f.manager.memory.Counters(cfg.Companion, now)
	if count != 0 {
		t.Fatal("quiet period charged")
	}
	// Outside quiet hours, only a selected, idle thread may claim the task;
	// the claim is persisted before execution and is not offered twice.
	cfg.Companion.QuietStart = "00:00"
	cfg.Companion.QuietEnd = "00:00"
	f.manager.mu.Lock()
	f.manager.config = cfg.Config
	f.manager.mu.Unlock()
	if err := f.manager.memory.Observe(p.trace, cfg.Companion, now); err != nil {
		t.Fatal(err)
	}
	if in, err := f.manager.claimAgentTask(now, func(string) bool { return true }); err != nil || in != nil {
		t.Fatal("busy thread claimed", in, err)
	}
	claimed, err := f.manager.claimAgentTask(now, func(string) bool { return false })
	if err != nil || claimed == nil || claimed.TaskID != task.ID {
		t.Fatal("task not resumed", claimed, err)
	}
	if again, err := f.manager.claimAgentTask(now, func(string) bool { return false }); err != nil || again != nil {
		t.Fatal("running task reclaimed", again, err)
	}
	f.manager.finishAgentTask(*claimed, "silent")
	if tasks := f.manager.agent.list("test", "wxid_owner"); tasks[0].State != "needs_review" {
		t.Fatal("uncheckpointed task repeated", tasks)
	}
}
