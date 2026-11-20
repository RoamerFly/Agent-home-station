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
func (n *friendReminderNative) LookupSend(_ context.Context, key string) (linuxnative.ToolStatus, error) {
	for _, request := range n.requests {
		if request.IdempotencyKey == key {
			out := n.status
			out.ThreadID = request.ThreadID
			return out, nil
		}
	}
	return linuxnative.ToolStatus{}, linuxnative.ErrToolNotFound
}
func memberReminder(t *testing.T) (*webFixture, *permittedTools, *friendReminderNative) {
	t.Helper()
	f, p := trustedAgent(t)
	n := &friendReminderNative{fakeTools: &fakeTools{fakeNative: f.native}, contacts: []map[string]any{{"id": "wxid_member", "relation": "unknown"}}, status: linuxnative.ToolStatus{ID: "reminder-send", ThreadID: "room@chatroom", State: "queued"}}
	f.manager.native = n
	f.manager.mu.Lock()
	f.manager.config.Enabled = true
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.PrivateAllFriends = true
	f.manager.config.Behavior.DelayMinSeconds = 1
	f.manager.config.Behavior.DelayMaxSeconds = 1
	f.manager.config.Companion.Permissions.Proactive = false
	f.manager.config.Companion.QuietStart = "00:00"
	f.manager.config.Companion.QuietEnd = "23:59"
	f.manager.config.GroupRules = []GroupRule{{ID: "room@chatroom", Mode: "social"}}
	f.manager.mu.Unlock()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "wxid_member", TraceID: "synthetic-reminder", Mode: "direct", IsGroup: true})
	p.trace.Text = "请提醒喝水"
	p.trace.Source = &companion.SourceRef{InstanceID: f.manager.memory.SourceInstanceID(), MessageID: p.trace.TraceID, ServerID: "reminder-source", Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), Revision: 1, Integrity: "verified"}
	if err := f.manager.memory.Observe(p.trace, f.manager.Config().Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	if source, ok := f.manager.memory.SourceForInput(p.trace); ok {
		p.trace.Source = source
	} else {
		t.Fatal("synthetic reminder source missing")
	}
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

func TestOwnerReminderAcceptedReceiptCreatesResumableAgentFollowUp(t *testing.T) {
	f, p, n := memberReminder(t)
	f.manager.agent.mu.Lock()
	f.manager.agent.settings.OwnerIDs = []string{"wxid_member"}
	f.manager.agent.settings.PrivilegedThreads = []string{"*"}
	f.manager.agent.mu.Unlock()
	task := createReminder(t, p, 1)
	f.manager.tickReminders(context.Background(), task.Next.Add(time.Second))
	if len(n.requests) != 1 {
		t.Fatal("reminder was not submitted", n.requests)
	}
	n.status.State = "accepted"
	n.status.ServerID = "owner-reminder-receipt"
	saved := f.manager.agent.list("test", task.Thread)[0]
	f.manager.tickReminders(context.Background(), saved.Next.Add(3*time.Hour))
	tasks := f.manager.agent.list("test", task.Thread)
	var reminder, followUp *agentTask
	for i := range tasks {
		candidate := tasks[i]
		if candidate.ID == task.ID {
			reminder = &candidate
		}
		if candidate.ParentID == task.ID {
			followUp = &candidate
		}
	}
	if reminder == nil || reminder.State != "completed" || reminder.Reminder.FollowUpTaskID == "" {
		t.Fatal("accepted reminder was not completed and linked", tasks)
	}
	if followUp == nil || followUp.State != "pending" || !followUp.OwnerRequested || followUp.RequestedText != "喝水" {
		t.Fatal("owner reminder did not create a resumable follow-up", tasks)
	}
	if followUp.Constraints == nil || followUp.Constraints.Purpose != "reminder_followup" {
		t.Fatal("follow-up constraints missing", followUp.Constraints)
	}
	if second, err := f.manager.scheduleReminderFollowUp(*reminder); err != nil || second != followUp.ID {
		t.Fatal("follow-up was not idempotent", second, err)
	}
	if len(f.manager.agent.list("test", task.Thread)) != 2 {
		t.Fatal("duplicate follow-up created")
	}
}

func TestReminderDueEntersExecutionRoundWithoutNativeReceipt(t *testing.T) {
	f, p, n := memberReminder(t)
	f.manager.agent.mu.Lock()
	f.manager.agent.settings.OwnerIDs = []string{"wxid_member"}
	f.manager.agent.settings.PrivilegedThreads = []string{"*"}
	f.manager.agent.mu.Unlock()
	task := createReminder(t, p, 3600)
	f.manager.processDueReminders(context.Background(), task.Next.Add(time.Hour), false)
	if len(n.requests) != 0 {
		t.Fatal("due reminder sent a native message", n.requests)
	}
	tasks := f.manager.agent.list("test", task.Thread)
	var followUp *agentTask
	for i := range tasks {
		if tasks[i].ParentID == task.ID {
			followUp = &tasks[i]
		}
	}
	if followUp == nil || !followUp.ReminderDue || followUp.State != "pending" || followUp.RequestedText != "喝水" {
		t.Fatal("due reminder did not enter execution round", tasks)
	}
	if len(tasks) != 2 || tasks[0].Reminder == nil && tasks[1].Reminder == nil {
		t.Fatal("original reminder record was not retained", tasks)
	}
}

func TestExecuteReminderResolvesNamedGroupAndKeepsOrigin(t *testing.T) {
	f, p, native := memberReminder(t)
	cfg := f.manager.agent.config()
	cfg.OwnerIDs = []string{p.trace.Sender}
	cfg.PrivilegedThreads = []string{p.trace.Thread}
	cfg.CrossConversation = true
	cfg.ReadConversations = true
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	native.contacts = []map[string]any{
		{"roomId": "test-group@chatroom", "displayName": "机器人测试群"},
		{"roomId": "test-group-2@chatroom", "displayName": "机器人测试群2"},
	}
	args := json.RawMessage(`{"action":"create","kind":"execute","text":"做一首歌并发到机器人测试群","request_key":"scheduled-song","delay_seconds":60,"delivery_group":"机器人测试群"}`)
	out, err := p.Call(t.Context(), "agent_reminder", args)
	if err != nil || !strings.Contains(out, `"result_group":"test-group@chatroom"`) {
		t.Fatal(out, err)
	}
	tasks := f.manager.agent.list("test", p.trace.Thread)
	if len(tasks) != 1 {
		t.Fatal("reminder was not saved once")
	}
	reminder := tasks[0]
	bytes, err := os.ReadFile(filepath.Join(f.manager.agent.dir, "reminders", reminder.ID+".json"))
	if err != nil || !strings.Contains(string(bytes), "test-group@chatroom") {
		t.Fatal("group was not persisted in the reminder file", err)
	}
	native.contacts = append(native.contacts, map[string]any{"roomId": "duplicate@chatroom", "displayName": "机器人测试群"})
	if _, err := p.resolveNamedGroup(t.Context(), "机器人测试群"); err == nil {
		t.Fatal("ambiguous name accepted")
	}
	f.manager.processDueReminders(t.Context(), reminder.Next.Add(time.Second), false)
	busyThreads := []string{}
	in, err := f.manager.claimAgentTask(reminder.Next.Add(time.Second), func(thread string) bool {
		busyThreads = append(busyThreads, thread)
		// The private request conversation has an unrelated pending/receipt
		// plan, but the selected delivery group is idle.  A due cross-session
		// reminder must still enter its execution round in this case.
		return thread == p.trace.Thread
	})
	if err != nil || in == nil || in.Thread != p.trace.Thread || !in.SilentExecution ||
		!strings.Contains(in.Text, "test-group@chatroom") || len(native.requests) != 0 {
		t.Fatal("wrong due execution", in, err)
	}
	if len(busyThreads) != 1 || busyThreads[0] != "test-group@chatroom" {
		t.Fatal("cross-session reminder checked the request thread for busy state", busyThreads)
	}
	p.SetTrace(*in)
	if !p.taskDeliveryAllowed("test-group@chatroom") || p.taskDeliveryAllowed("test-group-2@chatroom") ||
		f.manager.taskReplyReady(*in, companion.Decision{Action: "reply", Messages: []string{"准备中"}}) {
		t.Fatal("execution could reply outside the selected group")
	}
}

func TestReminderOverdueAtStartupExpiresWithoutRescheduling(t *testing.T) {
	f, p, n := memberReminder(t)
	task := createReminder(t, p, 3600)
	now := time.Now()
	due := now.Add(-time.Minute)
	f.manager.agent.mu.Lock()
	stored := f.manager.agent.data.Tasks[task.ID]
	stored.Reminder.Due = due
	stored.Next = stored.Reminder.Due
	f.manager.agent.data.Tasks[task.ID] = stored
	f.manager.agent.mu.Unlock()
	f.manager.prepareReminderSchedule(now)
	f.manager.prepareReminderSchedule(now.Add(time.Minute))
	got := f.manager.agent.list("test", task.Thread)[0]
	if got.State != "expired" || !got.Next.Equal(due) || !got.Reminder.Due.Equal(due) || len(n.requests) != 0 {
		t.Fatal("overdue startup reminder was executed automatically", got, n.requests)
	}
	if _, pending := f.manager.nextReminderDue(); pending {
		t.Fatal("expired reminder was scheduled again")
	}
	raw, err := os.ReadFile(filepath.Join(f.manager.agent.dir, "reminders", task.ID+".json"))
	var saved agentTask
	if err != nil || json.Unmarshal(raw, &saved) != nil || saved.State != "expired" || !saved.Reminder.Due.Equal(due) {
		t.Fatalf("expired reminder was not retained: %v", err)
	}
	f.login()
	code, _, _ := f.request("POST", "/api/agent/tasks/"+task.ID, map[string]string{"action": "resume"}, true, true)
	if code != 409 {
		t.Fatalf("expired reminder could be resumed: HTTP %d", code)
	}
}

func TestReminderStartupKeepsPendingReceiptAndRecoversFollowUp(t *testing.T) {
	for _, mode := range []string{"submitted", "handoff", "legacy-overdue"} {
		t.Run(mode, func(t *testing.T) {
			f, p, n := memberReminder(t)
			task := createReminder(t, p, 3600)
			expected := "expired"
			switch mode {
			case "submitted":
				task.State, task.Reminder.Job = "waiting_delivery", "synthetic-send"
				expected = "waiting_delivery"
			case "handoff":
				if _, err := f.manager.scheduleReminderFollowUp(task); err != nil {
					t.Fatal(err)
				}
				expected = "completed"
			case "legacy-overdue":
				task.State = "needs_review"
				task.LastResult = "提醒在工作站离线期间已到期，未自动执行；等待主人在执行回合中手动决定"
			}
			if err := f.manager.agent.task(task); err != nil {
				t.Fatal(err)
			}
			f.manager.prepareReminderSchedule(task.Reminder.Due.Add(time.Minute))
			got, _ := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
			if got.State != expected || got.Reminder.Job != task.Reminder.Job || len(n.requests) != 0 {
				t.Fatalf("unexpected restart result: state=%s job=%s", got.State, got.Reminder.Job)
			}
			if mode == "handoff" && got.Reminder.FollowUpTaskID != agentID("reminder-followup", task.ID) {
				t.Fatal("saved execution round was not linked")
			}
		})
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

func TestReminderUpdatePreservesIdentityAndPersists(t *testing.T) {
	f, p, n := memberReminder(t)
	original := createReminder(t, p, 7200)
	due := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	args, _ := json.Marshal(map[string]any{"action": "update", "id": original.ID, "run_at": due.Format(time.RFC3339)})
	if _, err := p.Call(context.Background(), "agent_reminder", args); err != nil {
		t.Fatal(err)
	}
	saved, ok := f.manager.agent.lookupTask(original.ID, original.Account, original.Thread)
	if !ok || !saved.Next.Equal(due) || !saved.Reminder.Due.Equal(due) || saved.RequestedText != original.RequestedText || saved.Reminder.Target != original.Reminder.Target {
		t.Fatal("update lost original identity or schedule")
	}
	raw, err := os.ReadFile(filepath.Join(f.manager.agent.dir, "reminders", original.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk agentTask
	if json.Unmarshal(raw, &disk) != nil || !disk.Reminder.Due.Equal(due) {
		t.Fatal("reminder file not updated")
	}
	if len(n.requests) != 0 {
		t.Fatal("reschedule sent a message")
	}
	if err := f.manager.rescheduleTask(original, due.Add(time.Hour)); err == nil {
		t.Fatal("stale update accepted")
	}
	p.trace.Sender = "wxid_other"
	if _, err := p.callReminder(context.Background(), args); err == nil {
		t.Fatal("other sender changed reminder")
	}
}

func TestOwnerCanFindAndEditOriginalReminderWithoutRecreatingIt(t *testing.T) {
	f, p, native := memberReminder(t)
	sender, group := p.trace.Sender, p.trace.Thread
	cfg := f.manager.agent.config()
	cfg.OwnerIDs, cfg.PrivilegedThreads = []string{sender}, []string{"*"}
	cfg.CrossConversation, cfg.ReadConversations = true, true
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	f.manager.config.PrivateEnabled = true
	f.manager.config.DirectIDs = []string{sender}
	native.contacts = []map[string]any{{"roomId": "other@chatroom", "displayName": "验收群"}}
	create := `{"action":"create","kind":"execute","text":"准备音乐","delay_seconds":7200,"request_key":"music"}`
	first, err := p.Call(t.Context(), "agent_reminder", json.RawMessage(create))
	if err != nil {
		t.Fatal(err)
	}
	again, err := p.Call(t.Context(), "agent_reminder", json.RawMessage(strings.Replace(create, `"music"`, `"generated-new-key"`, 1)))
	if err != nil || !strings.Contains(again, `"existing":true`) || len(f.manager.agent.active(p.trace.Account, group)) != 1 {
		t.Fatal("retry made duplicate reminder", first, again, err)
	}
	original := f.manager.agent.active(p.trace.Account, group)[0]
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: sender, Sender: sender, TraceID: "private-query"})
	p.routeCrossConversationThread(group)
	if _, err := p.Call(t.Context(), "agent_reminder", json.RawMessage(create)); err == nil {
		t.Fatal("reading another conversation recreated its reminder")
	}
	list, err := p.Call(t.Context(), "agent_reminder", json.RawMessage(`{"action":"list","scope":"account","query":"准备音乐","limit":1}`))
	if err != nil || !strings.Contains(list, original.ID) || !strings.Contains(list, `"total":1`) {
		t.Fatal(list, err)
	}
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: sender, Sender: sender, TraceID: "private-edit"})
	edit, _ := json.Marshal(map[string]any{
		"action": "update", "scope": "account", "id": original.ID,
		"title": "更新标题", "text": "改用新歌曲", "delivery_group": "验收群", "delay_seconds": 10800,
	})
	if _, err := p.Call(t.Context(), "agent_reminder", edit); err != nil {
		t.Fatal(err)
	}
	after, ok := f.manager.agent.lookupTask(original.ID, original.Account, group)
	if !ok || after.OriginTrace != original.OriginTrace || after.Thread != original.Thread || after.Title != "更新标题" ||
		after.RequestedText != "改用新歌曲" || after.Reminder.ResultGroup != "other@chatroom" ||
		!after.Next.After(original.Next) || len(native.requests) > 0 {
		t.Fatal("edit changed origin, lost fields or sent a message", after)
	}
}
