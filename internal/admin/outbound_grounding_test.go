package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func observeOutboundSource(t *testing.T, f *webFixture, p *permittedTools, request ...string) {
	t.Helper()
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.mu.Unlock()
	p.trace.Text = "请稍后提醒喝水"
	if len(request) > 0 {
		p.trace.Text = request[0]
	}
	p.trace.TraceID += "-outbound-source"
	p.trace.Source = &companion.SourceRef{InstanceID: f.manager.memory.SourceInstanceID(), MessageID: p.trace.TraceID, ServerID: "synthetic-server", Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), Revision: 1, Integrity: "verified"}
	if err := f.manager.memory.Observe(p.trace, f.manager.Config().Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	source, ok := f.manager.memory.SourceForInput(p.trace)
	if !ok {
		t.Fatal("missing source")
	}
	p.trace.Source = source
}
func TestForgottenReminderSourceStopsNewSubmission(t *testing.T) {
	f, p, n := memberReminder(t)
	observeOutboundSource(t, f, p)
	task := createReminder(t, p, 1)
	if task.Constraints == nil || task.Constraints.Source == nil {
		t.Fatal("reminder lost source binding")
	}
	if err := f.manager.memory.Clear(companion.ScopeID(p.trace.Channel, p.trace.Account, p.trace.Thread)); err != nil {
		t.Fatal(err)
	}
	f.manager.tickReminders(context.Background(), task.Next.Add(time.Second))
	got := f.manager.agent.list(task.Account, task.Thread)[0]
	if len(n.requests) != 0 || got.State != "needs_review" {
		t.Fatal("forgotten reminder submitted", len(n.requests), got.State)
	}
}

func TestLegacyReminderWithoutSourcePausesInsteadOfBlindSubmission(t *testing.T) {
	f, p, n := memberReminder(t)
	task := agentTask{ID: "legacy-unverified", Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, State: "waiting", Next: time.Now().Add(-time.Minute), Reminder: &reminderSpec{Target: p.trace.Thread, Due: time.Now().Add(-time.Minute), Text: "旧提醒"}}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	f.manager.tickReminders(context.Background(), time.Now())
	stored := f.manager.agent.list(task.Account, task.Thread)[0]
	if len(n.requests) != 0 || stored.State != "needs_review" {
		t.Fatal(len(n.requests), stored.State)
	}
}

type syntheticOutboundReviewer struct{ calls *int }

func (s syntheticOutboundReviewer) Chat(context.Context, string, []map[string]string) (string, error) {
	*s.calls++
	return `{"action":"reply","messages":["改写后的提醒"],"reply_contexts":[]}`, nil
}
func TestHighRiskReminderIsReviewedAndNotSilentlyRewritten(t *testing.T) {
	f, p, n := memberReminder(t)
	calls := 0
	p.outboundReviewer = syntheticOutboundReviewer{&calls}
	args, _ := json.Marshal(map[string]any{"action": "create", "text": "你爸心里有点空", "delay_seconds": 1, "request_key": "needs-review"})
	_, err := p.Call(context.Background(), "agent_reminder", args)
	if err == nil || calls != 1 || len(n.requests) != 0 || len(f.manager.agent.list(p.trace.Account, p.trace.Thread)) != 0 {
		t.Fatal("risky reminder bypassed review or was silently rewritten", err, calls, len(n.requests))
	}
}
func TestReminderAcceptedReceiptWinsAfterForgetAndLostCheckpoint(t *testing.T) {
	f, p, n := memberReminder(t)
	observeOutboundSource(t, f, p)
	task := createReminder(t, p, 1)
	n.requests = []linuxnative.ToolSend{{ThreadID: task.Reminder.Target, IdempotencyKey: "reminder-" + task.ID}}
	n.status = linuxnative.ToolStatus{ID: "already-sent", State: "accepted", ServerID: "receipt"}
	if err := f.manager.memory.Clear(companion.ScopeID(p.trace.Channel, p.trace.Account, p.trace.Thread)); err != nil {
		t.Fatal(err)
	}
	f.manager.tickReminders(context.Background(), task.Next.Add(time.Second))
	got := f.manager.agent.list(task.Account, task.Thread)[0]
	if len(n.requests) != 1 || got.State != "completed" || got.Reminder.ServerID != "receipt" {
		t.Fatal("receipt lost or duplicate send", len(n.requests), got.State)
	}
}
func TestExplicitSendCannotBypassForgetAndDoesNotReplayAcceptedKey(t *testing.T) {
	f, p := trustedAgent(t)
	c := f.manager.agent.config()
	c.CrossConversation = true
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	n := &friendReminderNative{fakeTools: &fakeTools{fakeNative: f.native}, status: linuxnative.ToolStatus{ID: "sent", State: "accepted", ServerID: "verified"}}
	f.manager.native = n
	observeOutboundSource(t, f, p)
	args, _ := json.Marshal(map[string]string{"thread_id": p.trace.Thread, "text": "合成测试", "request_key": "explicit-once"})
	key := p.operationID("wechat_send_text", args)
	if err := f.manager.memory.Clear(companion.ScopeID(p.trace.Channel, p.trace.Account, p.trace.Thread)); err != nil {
		t.Fatal(err)
	}
	blocked, _ := p.Call(context.Background(), "wechat_send_text", args)
	var rejection struct {
		State, Code, Stage, Instruction string
		NotStarted                      bool `json:"not_started"`
	}
	if err := json.Unmarshal([]byte(blocked), &rejection); err != nil || len(n.requests) != 0 || rejection.State != "needs_review" || rejection.Code == "" || rejection.Stage != "outbound_evidence" || !rejection.NotStarted || strings.Contains(rejection.Instruction, "新request_key") {
		t.Fatal("forgotten evidence was sent or misreported as a transport retry", blocked, err)
	}
	// A separate original operation with an already accepted native receipt is
	// reconciled before examining its now-forgotten source; no new Submit.
	args, _ = json.Marshal(map[string]string{"thread_id": p.trace.Thread, "text": "合成测试", "request_key": "accepted-once"})
	key = p.operationID("wechat_send_text", args)
	n.requests = []linuxnative.ToolSend{{ThreadID: p.trace.Thread, IdempotencyKey: key}}
	result, err := p.Call(context.Background(), "wechat_send_text", args)
	if err != nil || len(n.requests) != 1 {
		t.Fatal(result, err, len(n.requests))
	}
}

type uncertainOutboundLookup struct{ *friendReminderNative }

func (*uncertainOutboundLookup) LookupSend(context.Context, string) (linuxnative.ToolStatus, error) {
	return linuxnative.ToolStatus{}, errors.New("synthetic receipt connection unavailable")
}

func TestUnknownOutboundReceiptIsNotReportedAsDefinitelyUnsubmitted(t *testing.T) {
	f, p := trustedAgent(t)
	settings := f.manager.agent.config()
	settings.CrossConversation = true
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	native := &uncertainOutboundLookup{&friendReminderNative{fakeTools: &fakeTools{fakeNative: f.native}}}
	f.manager.native = native
	args := json.RawMessage(`{"thread_id":"wxid_owner","text":"合成结果","request_key":"unknown-original-key"}`)
	result, err := p.Call(t.Context(), "wechat_send_text", args)
	var status struct {
		State      string
		NotStarted bool `json:"not_started"`
	}
	if err != nil || json.Unmarshal([]byte(result), &status) != nil || status.NotStarted || status.State != "waiting" || len(native.requests) != 0 {
		t.Fatal("unknown earlier delivery was asserted not started", result, err)
	}
}
