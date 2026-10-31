package admin

import (
	"context"
	"encoding/json"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func explicitConversationFixture(t *testing.T) (*webFixture, *permittedTools, *friendReminderNative, *int) {
	t.Helper()
	f, p, n := memberReminder(t)
	p.trace.SenderName, p.trace.SenderNameSource = "Beta", "native"
	observeOutboundSource(t, f, p, "请向当前会话发送一段合成内容")
	text := "另一位成员的合成消息"
	other := companion.Input{Channel: p.trace.Channel, Account: p.trace.Account, Thread: p.trace.Thread, Sender: "member-alpha", SenderName: "Alpha", SenderNameSource: "native", TraceID: "known-alpha", Text: text, OriginalText: &text, MessageTime: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := f.manager.memory.Observe(other, f.manager.Config().Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	p.outboundReviewer = syntheticOutboundReviewer{calls: &calls}
	return f, p, n, &calls
}

func TestExplicitConversationDeliveryDoesNotImpersonateSourceMemberReply(t *testing.T) {
	_, p, _, calls := explicitConversationFixture(t)
	text, binding, err := p.prepareOutboundText(context.Background(), p.trace.Thread, "Alpha，晚安")
	if err != nil || text != "Alpha，晚安" {
		t.Fatal("explicit conversation delivery was treated as a reply to Beta", text, err)
	}
	in := p.outboundInput(p.trace.Thread)
	if !in.ConversationDelivery || in.SourceOnlyContext {
		t.Fatal("same conversation delivery identity was not explicit", in.ConversationDelivery, in.SourceOnlyContext)
	}
	if err := p.validateOutboundText(context.Background(), p.trace.Thread, binding, text); err != nil {
		t.Fatal("final exact text rejected", err)
	}
	if *calls != 0 {
		t.Fatal("explicit conversation routing added a model call", *calls)
	}
	changed := in
	changed.ConversationDelivery = false
	e := &companion.Engine{Config: p.manager.Config().Companion, Store: p.manager.memory, Tools: p}
	if err := e.ValidateReplyText(context.Background(), changed, binding, text); err == nil {
		t.Fatal("conversation delivery flag was not bound into the proof")
	}
}

func TestReminderReconstructionKeepsConversationDeliveryAndExactText(t *testing.T) {
	f, p, n, _ := explicitConversationFixture(t)
	args, _ := json.Marshal(map[string]any{"action": "create", "text": "Alpha，晚安", "delay_seconds": 1, "request_key": "explicit-person-greeting"})
	if _, err := p.Call(context.Background(), "agent_reminder", args); err != nil {
		t.Fatal(err)
	}
	task := f.manager.agent.list(p.trace.Account, p.trace.Thread)[0]
	if !reminderInput(task).ConversationDelivery || task.Reminder.Binding == nil {
		t.Fatal("reminder lost delivery identity")
	}
	restored, err := newAgentControl(f.manager.agent.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.agent = restored
	if err := validateReminderSource(context.Background(), f.manager, task, task.Reminder.Text); err != nil {
		t.Fatal("restored explicit reminder rejected", err)
	}
	f.manager.tickReminders(context.Background(), task.Next.Add(time.Second))
	if len(n.requests) != 1 || n.requests[0].Text != task.Reminder.Text {
		t.Fatal("explicit reminder did not use its checked exact text", n.requests)
	}
}
