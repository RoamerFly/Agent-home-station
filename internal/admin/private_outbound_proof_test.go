package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func reviseOutboundSource(t *testing.T, f *webFixture, p *permittedTools) {
	t.Helper()
	in := p.trace
	source := *in.Source
	source.Revision++
	in.Source = &source
	in.Text += "（用户更正）"
	in.OriginalText = &in.Text
	if err := f.manager.memory.Observe(in, f.manager.Config().Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func revokeOutboundEvidence(t *testing.T, f *webFixture, p *permittedTools, change string) {
	t.Helper()
	switch change {
	case "forget":
		if err := f.manager.memory.Clear(companion.ScopeID(p.trace.Channel, p.trace.Account, p.trace.Thread)); err != nil {
			t.Fatal(err)
		}
	case "revise":
		reviseOutboundSource(t, f, p)
	case "owner", "cross_conversation":
		settings := f.manager.agent.config()
		if change == "owner" {
			settings.OwnerIDs = []string{"different-owner"}
		} else {
			settings.CrossConversation = false
		}
		if err := f.manager.agent.save(settings); err != nil {
			t.Fatal(err)
		}
	case "remember":
		f.manager.mu.Lock()
		f.manager.config.Companion.Permissions.Remember = false
		f.manager.mu.Unlock()
	}
}

func TestPrivateOutboundPreparationBindsEvidenceWithoutModelCall(t *testing.T) {
	for _, change := range []string{"forget", "revise", "owner", "cross_conversation", "remember"} {
		t.Run(change, func(t *testing.T) {
			f, p := globalSearchFixture(t)
			observeOutboundSource(t, f, p)
			calls := 0
			p.outboundReviewer = syntheticOutboundReviewer{&calls}
			text, binding, err := p.prepareOutboundText(t.Context(), p.trace.Thread, "合成私聊结果")
			if err != nil || text != "合成私聊结果" || binding.EvidenceEpoch == "" || binding.EvidenceAuthorization == "" || calls != 0 {
				t.Fatal("private preparation lost binding or called semantic model", text, binding, calls, err)
			}
			original := binding
			revokeOutboundEvidence(t, f, p, change)
			if err := p.validateOutboundText(t.Context(), p.trace.Thread, binding); err == nil {
				t.Fatal("private outgoing proof survived evidence change", change)
			}
			if binding.ContextVersion != original.ContextVersion || binding.EvidenceEpoch != original.EvidenceEpoch || binding.EvidenceAuthorization != original.EvidenceAuthorization {
				t.Fatal("validation rewrote original proof")
			}
		})
	}
}

func TestPrivateOutboundCannotRemintOldOrLegacyTaskEvidence(t *testing.T) {
	for _, change := range []string{"revise", "owner", "legacy"} {
		t.Run(change, func(t *testing.T) {
			f, p := globalSearchFixture(t)
			observeOutboundSource(t, f, p)
			_, binding, err := p.prepareOutboundText(t.Context(), p.trace.Thread, "合成私聊结果")
			if err != nil {
				t.Fatal(err)
			}
			p.trace.ReplyContext = &binding
			if change == "legacy" {
				binding.EvidenceEpoch, binding.EvidenceAuthorization = "", ""
			} else {
				revokeOutboundEvidence(t, f, p, change)
			}
			if _, _, err = p.prepareOutboundText(t.Context(), p.trace.Thread, "续办中的合成结果"); err == nil {
				t.Fatal("old context acquired fresh evidence grant", change)
			}
		})
	}
}

func TestPrivateReminderPersistsPreparedProofAndStopsAfterEvidenceChange(t *testing.T) {
	for _, destination := range []string{"current", "private"} {
		for _, change := range []string{"forget", "revise", "owner", "cross_conversation", "remember"} {
			t.Run(destination+"/"+change, func(t *testing.T) {
				f, p, n := memberReminder(t)
				settings := f.manager.agent.config()
				settings.OwnerIDs = []string{p.trace.Sender}
				settings.PrivilegedThreads = []string{"*"}
				settings.CrossConversation = true
				if err := f.manager.agent.save(settings); err != nil {
					t.Fatal(err)
				}
				if destination == "current" {
					p.trace.Thread, p.trace.IsGroup = p.trace.Sender, false
					p.trace.Source = nil
					f.manager.mu.Lock()
					f.manager.config.DirectIDs = append(f.manager.config.DirectIDs, p.trace.Thread)
					f.manager.mu.Unlock()
					observeOutboundSource(t, f, p)
				}
				calls := 0
				p.outboundReviewer = syntheticOutboundReviewer{&calls}
				args, _ := json.Marshal(map[string]any{"action": "create", "text": "喝水", "delay_seconds": 1, "request_key": "private-proof", "destination": destination})
				if _, err := p.Call(t.Context(), "agent_reminder", args); err != nil {
					t.Fatal(err)
				}
				task := f.manager.agent.list(p.trace.Account, p.trace.Thread)[0]
				if task.Reminder.Binding == nil || task.Reminder.Binding.EvidenceEpoch == "" || task.Reminder.Binding.EvidenceAuthorization == "" || calls != 0 {
					t.Fatal("private reminder did not persist production proof", task.Reminder.Binding, calls)
				}
				if err := validateReminderSource(t.Context(), f.manager, task); err != nil {
					t.Fatal("fresh reminder source rejected", err)
				}
				restored, err := newAgentControl(f.manager.agent.dir)
				if err != nil {
					t.Fatal(err)
				}
				f.manager.agent = restored
				revokeOutboundEvidence(t, f, p, change)
				f.manager.tickReminders(t.Context(), task.Next.Add(time.Second))
				got := f.manager.agent.list(task.Account, task.Thread)[0]
				if got.State != "needs_review" || len(n.requests) != 0 {
					t.Fatal("private resumed reminder bypassed changed evidence", got.State, len(n.requests))
				}
			})
		}
	}
}

func TestLegacyPrivateReminderMissingEvidenceStampPauses(t *testing.T) {
	f, p, n := memberReminder(t)
	p.trace.Thread, p.trace.IsGroup = p.trace.Sender, false
	p.trace.Source = nil
	f.manager.mu.Lock()
	f.manager.config.DirectIDs = append(f.manager.config.DirectIDs, p.trace.Thread)
	f.manager.mu.Unlock()
	observeOutboundSource(t, f, p)
	task := createReminder(t, p, 1)
	task.Reminder.Binding.EvidenceEpoch = ""
	task.Reminder.Binding.EvidenceAuthorization = ""
	bound := mintOutboundBinding(reminderInput(task), f.manager.Config().Companion, *task.Reminder.Binding)
	task.Reminder.Binding = &bound
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	if err := validateReminderSource(t.Context(), f.manager, task); err == nil || !strings.Contains(err.Error(), "legacy_outbound") {
		t.Fatal("legacy private stamp was silently trusted", err)
	}
	f.manager.tickReminders(t.Context(), task.Next.Add(time.Second))
	if got := f.manager.agent.list(task.Account, task.Thread)[0]; got.State != "needs_review" || len(n.requests) != 0 {
		t.Fatal("legacy private reminder was submitted", got.State, len(n.requests))
	}
}
