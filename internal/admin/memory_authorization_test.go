package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestRetainedMemoryAuthorizationRevocation(t *testing.T) {
	for _, tool := range []string{"memory_read", "memory_search", "agent_global_search"} {
		for _, permission := range []string{"owner", "cross_conversation", "remember"} {
			for _, phase := range []string{"before_retention", "after_retention"} {
				t.Run(tool+"/"+permission+"/"+phase, func(t *testing.T) {
					f, p := globalSearchFixture(t)
					in := companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", Sender: "member", TraceID: "synthetic-authorization", Text: "synthetic private source", Source: &companion.SourceRef{MessageID: "synthetic-authorization"}}
					if err := f.manager.memory.Observe(in, f.manager.Config().Companion, time.Now()); err != nil {
						t.Fatal(err)
					}
					source, ok := f.manager.memory.SourceForInput(in)
					if !ok || source.EventID == "" {
						t.Fatal("missing synthetic source")
					}
					args := json.RawMessage(`{"thread_id":"source@chatroom","originals":true}`)
					if tool == "memory_read" {
						args, _ = json.Marshal(map[string]any{"thread_id": in.Thread, "event": true, "id": source.EventID})
					} else if tool == "agent_global_search" {
						args = json.RawMessage(`{"thread_id":"source@chatroom"}`)
					}
					result, err := p.Call(t.Context(), tool, args)
					if err != nil || !strings.Contains(result, in.Text) {
						t.Fatal("query", result, err)
					}
					var envelope struct {
						Authorization string `json:"_memory_authorization"`
					}
					if err = json.Unmarshal([]byte(result), &envelope); err != nil || !p.MemoryResultAuthorized(envelope.Authorization) {
						t.Fatal("query missing live authorization", result, err)
					}
					var id string
					if phase == "after_retention" {
						id, err = p.RetainToolOutput(t.Context(), tool, result)
						if err != nil || id == "" {
							t.Fatal(id, err)
						}
					}
					settings := f.manager.agent.config()
					switch permission {
					case "owner":
						settings.OwnerIDs = []string{"different-owner"}
					case "cross_conversation":
						settings.CrossConversation = false
					case "remember":
						f.manager.mu.Lock()
						f.manager.config.Companion.Permissions.Remember = false
						f.manager.mu.Unlock()
					}
					if err = f.manager.agent.save(settings); err != nil {
						t.Fatal(err)
					}
					if p.MemoryResultAuthorized(envelope.Authorization) {
						t.Fatal("old authorization still valid")
					}
					if phase == "before_retention" {
						if id, err = p.RetainToolOutput(t.Context(), tool, result); err == nil || id != "" {
							t.Fatal("old query granted fresh retention authorization", id, err)
						}
					} else if _, err = p.readToolOutput(t.Context(), "", json.RawMessage(`{"result_id":"`+id+`"}`)); err == nil {
						t.Fatal("revoked source remained readable")
					}
				})
			}
		}
	}
}

func TestMemoryQueryRejectsChangedAuthorizationWithoutEpochChange(t *testing.T) {
	f, p := globalSearchFixture(t)
	epoch, err := f.manager.memory.DataEpoch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stamp := p.memoryQueryAuthorization()
	settings := f.manager.agent.config()
	settings.CrossConversation = false
	if err = f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err = p.stampMemoryQuery(t.Context(), epoch, stamp, map[string]any{"text": "old query"}); err == nil {
		t.Fatal("query finishing after revocation acquired a fresh authorization")
	}
}

func TestCurrentConversationMemoryRemainsAvailableToNonOwner(t *testing.T) {
	f, p := globalSearchFixture(t)
	p.trace.Thread = "source@chatroom"
	p.trace.Sender = "member"
	settings := f.manager.agent.config()
	settings.CrossConversation = false
	if err := f.manager.agent.save(settings); err != nil {
		t.Fatal(err)
	}
	if p.privileged() {
		t.Fatal("fixture member is owner")
	}
	in := p.trace
	in.TraceID, in.Text = "synthetic-member-query", "synthetic current conversation memory"
	if err := f.manager.memory.Observe(in, f.manager.Config().Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err := p.Call(t.Context(), "memory_search", json.RawMessage(`{"originals":true}`))
	if err != nil || !strings.Contains(result, in.Text) {
		t.Fatal(result, err)
	}
	id, err := p.RetainToolOutput(t.Context(), "memory_search", result)
	if err != nil || id == "" {
		t.Fatal(id, err)
	}
	page, err := p.readToolOutput(t.Context(), "", json.RawMessage(`{"result_id":"`+id+`"}`))
	var envelope struct {
		Authorization string `json:"memory_authorization"`
	}
	if err != nil || json.Unmarshal([]byte(page), &envelope) != nil || !p.MemoryResultAuthorized(envelope.Authorization) {
		t.Fatal("member lost legitimate current-conversation page", page, err)
	}
	// Legacy retained results cannot receive current authorization retroactively.
	epoch, err := f.manager.memory.DataEpoch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := json.Marshal(map[string]string{"_memory_epoch": epoch, "text": "legacy"})
	if _, err = p.RetainToolOutput(t.Context(), "memory_search", string(legacy)); err == nil {
		t.Fatal("unstamped legacy memory granted authorization")
	}
}

func TestMemoryEvidenceAuthorizationInterfaceRejectsRevocation(t *testing.T) {
	for _, revoked := range []string{"owner", "cross_conversation"} {
		t.Run(revoked, func(t *testing.T) {
			f, p := globalSearchFixture(t)
			// The reply engine consumes this optional interface; a saved reply
			// keeps the original snapshot, never a newly granted replacement.
			var authority interface {
				MemoryResultAuthorization() string
				MemoryResultAuthorized(string) bool
			} = p
			bound := authority.MemoryResultAuthorization()
			if len(bound) != 64 || !authority.MemoryResultAuthorized(bound) {
				t.Fatal("missing opaque evidence authorization")
			}
			settings := f.manager.agent.config()
			if revoked == "owner" {
				settings.OwnerIDs = []string{"other-owner"}
			} else {
				settings.CrossConversation = false
			}
			if err := f.manager.agent.save(settings); err != nil {
				t.Fatal(err)
			}
			if authority.MemoryResultAuthorized(bound) {
				t.Fatal("delivery evidence still authorizes revoked visibility")
			}
			fresh := authority.MemoryResultAuthorization()
			if fresh == bound || !authority.MemoryResultAuthorized(fresh) {
				t.Fatal("current local memory authorization is unavailable")
			}
		})
	}
}

func TestOutboundEvidenceAuthorizationRejectsRevokedRecall(t *testing.T) {
	for _, revoked := range []string{"owner", "cross_conversation"} {
		t.Run(revoked, func(t *testing.T) {
			f, p := globalSearchFixture(t)
			observeOutboundSource(t, f, p)
			in := p.outboundInput(p.trace.Thread)
			_, binding, err := p.prepareOutboundText(t.Context(), in.Thread, "合成私聊结果")
			if err != nil || binding.EvidenceEpoch == "" || binding.EvidenceAuthorization == "" {
				t.Fatal("production preparation omitted evidence stamp", binding, err)
			}
			if err := p.validateOutboundText(t.Context(), in.Thread, binding); err != nil {
				t.Fatal("fresh source evidence rejected", err)
			}
			settings := f.manager.agent.config()
			if revoked == "owner" {
				settings.OwnerIDs = []string{"other-owner"}
			} else {
				settings.CrossConversation = false
			}
			if err := f.manager.agent.save(settings); err != nil {
				t.Fatal(err)
			}
			if !p.conversationReadAllowed(in.Thread) {
				t.Fatal("fixture lost independent destination permission")
			}
			if err := p.validateOutboundText(t.Context(), in.Thread, binding); err == nil || !strings.Contains(err.Error(), "reply_evidence_authorization_changed") {
				t.Fatal("private source delivery bypassed revoked evidence authorization", err)
			}
		})
	}
}

func TestReminderRecoveryPreservesOriginalEvidenceAuthorization(t *testing.T) {
	for _, revoked := range []string{"owner", "cross_conversation"} {
		t.Run(revoked, func(t *testing.T) {
			f, p, _ := memberReminder(t)
			settings := f.manager.agent.config()
			settings.OwnerIDs = []string{p.trace.Sender}
			settings.PrivilegedThreads = []string{"*"}
			settings.CrossConversation = true
			if err := f.manager.agent.save(settings); err != nil {
				t.Fatal(err)
			}
			task := createReminder(t, p, 1)
			binding := *task.Reminder.Binding
			if binding.EvidenceAuthorization == "" || binding.EvidenceEpoch == "" {
				t.Fatal("production reminder omitted evidence stamp", binding)
			}
			originalAuthorization := binding.EvidenceAuthorization
			if err := validateReminderSource(t.Context(), f.manager, task); err != nil {
				t.Fatal("fresh persisted evidence rejected", err)
			}
			settings = f.manager.agent.config()
			if revoked == "owner" {
				settings.OwnerIDs = []string{"other-owner"}
			} else {
				settings.CrossConversation = false
			}
			if err := f.manager.agent.save(settings); err != nil {
				t.Fatal(err)
			}
			if err := validateReminderSource(t.Context(), f.manager, task); err == nil || !strings.Contains(err.Error(), "reply_evidence_authorization_changed") {
				t.Fatal("recovered reminder reauthorized old evidence", err)
			}
			if task.Reminder.Binding.EvidenceAuthorization != originalAuthorization {
				t.Fatal("recovery mutated original stamp")
			}
		})
	}
}
