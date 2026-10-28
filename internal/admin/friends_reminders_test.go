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
	"wechat-bot/internal/linuxnative"
)

type friendReminderNative struct {
	*fakeTools
	contacts   []map[string]any
	catalogErr error
	requests   []linuxnative.ToolSend
	status     linuxnative.ToolStatus
}

func (*friendReminderNative) Ready() bool { return true }
func (n *friendReminderNative) Catalog(_ context.Context, kind, thread, query, cursor string) (linuxnative.CatalogPage, error) {
	if n.catalogErr != nil {
		return linuxnative.CatalogPage{}, n.catalogErr
	}
	return linuxnative.CatalogPage{Items: n.contacts}, nil
}
func (n *friendReminderNative) Submit(_ context.Context, r linuxnative.ToolSend) (linuxnative.ToolStatus, error) {
	n.requests = append(n.requests, r)
	out := n.status
	out.ThreadID = r.ThreadID
	return out, nil
}
func (n *friendReminderNative) ToolSendStatus(context.Context, string) (linuxnative.ToolStatus, error) {
	return n.status, nil
}
func memberReminder(t *testing.T) (*webFixture, *permittedTools, *friendReminderNative) {
	t.Helper()
	f, p := trustedAgent(t)
	n := &friendReminderNative{fakeTools: &fakeTools{fakeNative: f.native}, contacts: []map[string]any{{"id": "wxid_member", "relation": "unknown"}}, status: linuxnative.ToolStatus{ID: "reminder-send", ThreadID: "room@chatroom", State: "queued"}}
	f.manager.native = n
	f.manager.mu.Lock()
	f.manager.config.Enabled = true
	f.manager.config.Companion.Enabled = true
	f.manager.config.PrivateAllFriends = true
	f.manager.config.Behavior.DelayMinSeconds = 1
	f.manager.config.Behavior.DelayMaxSeconds = 1
	f.manager.config.Companion.Permissions.Proactive = false
	f.manager.config.Companion.QuietStart = "00:00"
	f.manager.config.Companion.QuietEnd = "23:59"
	f.manager.config.GroupRules = []GroupRule{{ID: "room@chatroom", Mode: "social"}}
	f.manager.mu.Unlock()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "wxid_member", TraceID: "synthetic-reminder", Mode: "direct", IsGroup: true})
	return f, p, n
}
func createReminder(t *testing.T, p *permittedTools, delay int64) agentTask {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"action": "create", "text": "喝水", "delay_seconds": delay, "request_key": "water"})
	out, e := p.Call(context.Background(), "agent_reminder", b)
	if e != nil {
		t.Fatal(e)
	}
	var result struct{ ID string }
	if json.Unmarshal([]byte(out), &result) != nil || result.ID == "" {
		t.Fatal(out)
	}
	for _, task := range p.manager.agent.list("test", p.trace.Thread) {
		if task.ID == result.ID {
			return task
		}
	}
	t.Fatal("no task")
	return agentTask{}
}
func TestFriendDirectoryAdmissionIsExactAndDoesNotGrantHostAccess(t *testing.T) {
	f, p, n := memberReminder(t)
	for _, row := range []map[string]any{{"id": "wxid_member", "relation": "unknown"}, {"username": "wxid_member", "relation": "friend"}} {
		n.contacts = []map[string]any{row}
		f.manager.friends = nil
		ok, e := f.manager.authorizeConversation(context.Background(), "wxid_member")
		if e != nil || !ok {
			t.Fatal("directory friend denied", e)
		}
	}
	for _, row := range []map[string]any{{"id": "other", "name": "wxid_member"}, {"id": "wxid_member", "relation": "stranger"}, {"id": "wxid_member", "deleted": true}} {
		n.contacts = []map[string]any{row}
		f.manager.friends = nil
		ok, e := f.manager.authorizeConversation(context.Background(), "wxid_member")
		if e != nil || ok {
			t.Fatal("unverified contact admitted", row, e)
		}
	}
	f.manager.friends = nil
	n.catalogErr = errors.New("offline")
	if ok, e := f.manager.authorizeConversation(context.Background(), "wxid_member"); ok || e == nil {
		t.Fatal("directory error silently skipped")
	}
	if ok, e := f.manager.authorizeConversation(context.Background(), "other@chatroom"); ok || e != nil {
		t.Fatal("groups opened")
	}
	p.SetTrace(companion.Input{Account: "test", Thread: "room@chatroom", Sender: "wxid_member"})
	if p.agentToolEnabled("wechat_conversations") || p.agentToolEnabled("agent_global_search") || !p.agentToolEnabled("agent_reminder") {
		t.Fatal("member privilege boundary")
	}
}
func TestMemberReminderPersistsAndDispatchesAfterRestartWithoutProactivePermission(t *testing.T) {
	f, p, n := memberReminder(t)
	task := createReminder(t, p, 7200)
	if task.OwnerRequested || task.Reminder == nil || time.Until(task.Next) < 119*time.Minute {
		t.Fatal("wrong schedule/privilege")
	}
	raw, e := os.ReadFile(filepath.Join(f.manager.agent.dir, "reminders", task.ID+".json"))
	if e != nil || !strings.Contains(string(raw), "喝水") {
		t.Fatal("no actual task file", e)
	}
	f.manager.tickReminders(context.Background(), time.Now())
	if len(n.requests) != 0 {
		t.Fatal("early reminder")
	}
	restored, e := newAgentControl(f.manager.agent.dir)
	if e != nil {
		t.Fatal(e)
	}
	f.manager.agent = restored
	if in, e := f.manager.claimAgentTask(task.Next.Add(time.Second), func(string) bool { return false }); in != nil || e != nil {
		t.Fatal("reminder consumed as proactive task", e)
	}
	f.manager.tickReminders(context.Background(), task.Next.Add(time.Second))
	if len(n.requests) != 1 || n.requests[0].Kind != "text" || n.requests[0].Text != "提醒你：喝水" {
		t.Fatal("explicit reminder not submitted", n.requests)
	}
	saved := f.manager.agent.list("test", task.Thread)[0]
	if saved.State != "waiting_delivery" || saved.Reminder.Job != "reminder-send" {
		t.Fatal("queued reported delivered", saved.State)
	}
	n.status.State = "accepted"
	n.status.ServerID = "999"
	f.manager.tickReminders(context.Background(), saved.Next.Add(3*time.Hour))
	saved = f.manager.agent.list("test", task.Thread)[0]
	if saved.State != "completed" || len(n.requests) != 1 || saved.Reminder.ServerID != "999" {
		t.Fatal("receipt/replay failure", saved.State, len(n.requests))
	}
	raw, _ = os.ReadFile(filepath.Join(f.manager.agent.dir, "reminders", task.ID+".json"))
	if !strings.Contains(string(raw), "completed") {
		t.Fatal("mirror stale")
	}
}
func TestMemberReminderScopeCancellationIdempotencyAndNoPromotion(t *testing.T) {
	f, p, n := memberReminder(t)
	task := createReminder(t, p, 7200)
	if again := createReminder(t, p, 7200); again.ID != task.ID || len(f.manager.agent.list("test", "")) != 1 {
		t.Fatal("duplicate reminder")
	}
	for _, args := range []string{`{"action":"create","text":"喝水","delay_seconds":3600,"request_key":"water"}`, `{"action":"create","text":"喝水","delay_seconds":0,"request_key":"bad"}`, `{"action":"create","text":"喝水","delay_seconds":2,"run_at":"2099-01-01T00:00:00+08:00","request_key":"bad"}`, `{"action":"create","text":"喝水","delay_seconds":2,"destination":"other@chatroom","request_key":"bad"}`} {
		if _, e := p.Call(context.Background(), "agent_reminder", json.RawMessage(args)); e == nil {
			t.Fatal("invalid scheduling accepted", args)
		}
	}
	b, _ := json.Marshal(map[string]any{"id": task.ID, "state": "pending", "checkpoint": "run code"})
	if _, e := p.Call(context.Background(), "agent_task", b); e == nil {
		t.Fatal("reminder promoted to arbitrary task")
	}
	p.trace.Sender = "wxid_other"
	out, e := p.Call(context.Background(), "agent_reminder", json.RawMessage(`{"action":"list"}`))
	if e != nil || strings.Contains(out, task.ID) {
		t.Fatal("other member tasks exposed", e)
	}
	out, e = p.Call(context.Background(), "agent_tasks", json.RawMessage(`{}`))
	if e != nil || strings.Contains(out, task.ID) {
		t.Fatal("alternate list leaked reminder", e)
	}
	b, _ = json.Marshal(map[string]string{"action": "cancel", "id": task.ID})
	if _, e = p.Call(context.Background(), "agent_reminder", b); e == nil {
		t.Fatal("cancelled another member task")
	}
	p.trace.Sender = "wxid_member"
	stale := f.manager.agent.list("test", task.Thread)[0]
	if _, e = p.Call(context.Background(), "agent_reminder", b); e != nil {
		t.Fatal(e)
	}
	if ok, e := f.manager.agent.reminderUpdate(stale); ok || e != nil {
		t.Fatal("late response resurrected cancellation", e)
	}
	f.manager.tickReminders(context.Background(), task.Next.Add(time.Hour))
	if len(n.requests) != 0 {
		t.Fatal("cancelled reminder sent")
	}
}
func TestMemberReminderPrivateTargetIsOnlyRequester(t *testing.T) {
	f, p, _ := memberReminder(t)
	out, e := p.Call(context.Background(), "agent_reminder", json.RawMessage(`{"action":"create","text":"休息","delay_seconds":7200,"request_key":"private","destination":"private"}`))
	if e != nil {
		t.Fatal(e)
	}
	tasks := f.manager.agent.list("test", p.trace.Thread)
	if len(tasks) != 1 || tasks[0].Reminder.Target != "wxid_member" {
		t.Fatal("wrong private target", out)
	}
	if p.agentToolEnabled("agent_global_search") || p.agentToolEnabled("wechat_conversations") {
		t.Fatal("reminder granted account-wide access")
	}
}
