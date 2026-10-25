package bot

import (
	"context"
	"strings"
	"time"
	"wechat-bot/internal/companion"
)

type conversationWake struct {
	Input        companion.Input `json:"input"`
	Parent       string          `json:"parent"`
	Due          time.Time       `json:"due"`
	State        string          `json:"state"`
	WaitForReply bool            `json:"wait_for_reply"`
}

func (w *Worker) scheduleConversationWake(c *companion.Engine, in companion.Input, seconds int, wait bool) {
	settings := c.Config.ConversationConfig()
	if w.runtime == nil || !settings.Enabled || !settings.AllowWake || seconds < settings.WakeMinSeconds || seconds > settings.WakeMaxSeconds || in.Surf || strings.HasPrefix(in.TraceID, "wake-") {
		return
	}
	selected := false
	for _, id := range c.Config.ProactiveThreads {
		selected = selected || id == in.Thread
	}
	if !selected || !c.Config.Permissions.Proactive {
		return
	}
	for _, wake := range w.runtime.Wakes {
		if wake.Input.Thread == in.Thread && (wake.State == "waiting" || wake.Parent == in.TraceID) {
			return
		}
	}
	if len(w.runtime.Wakes) >= 200 {
		kept := w.runtime.Wakes[:0]
		for _, wake := range w.runtime.Wakes {
			if wake.State == "waiting" || time.Since(wake.Due) < 24*time.Hour {
				kept = append(kept, wake)
			}
		}
		w.runtime.Wakes = kept
	}
	if len(w.runtime.Wakes) >= 200 {
		return
	}
	w.runtime.Wakes = append(w.runtime.Wakes, &conversationWake{Input: in, Parent: in.TraceID, Due: time.Now().Add(time.Duration(seconds) * time.Second), State: "waiting", WaitForReply: wait})
	w.persistErr = w.saveRuntime()
	w.activity(Record{Event: "conversation_wake_scheduled", Category: "decision", ThreadID: in.Thread, MessageID: in.TraceID, Status: "waiting", Reason: "model_requested_later_reassessment"})
}
func (w *Worker) tickConversationWake(ctx context.Context) bool {
	c, ok := w.model.(*companion.Engine)
	if !ok || w.runtime == nil || w.draining() {
		return false
	}
	for _, wake := range w.runtime.Wakes {
		if wake.State != "waiting" || time.Now().Before(wake.Due) {
			continue
		}
		if time.Since(wake.Due) > time.Duration(max(300, c.Config.ConversationConfig().WakeMaxSeconds))*time.Second {
			wake.State = "expired"
			w.persistErr = w.saveRuntime()
			continue
		}
		if c.Config.Quiet(time.Now()) {
			continue
		}
		in := wake.Input
		if w.generation != nil && (w.generation.jobs[in.Thread] != nil || !w.generation.canGenerate()) {
			continue
		}
		if !w.allowed[in.Thread] {
			wake.State = "stopped"
			w.persistErr = w.saveRuntime()
			continue
		}
		if err := c.Store.ClaimConversationWake(in, c.Config, time.Now()); err != nil {
			wake.State = "stopped"
			w.persistErr = w.saveRuntime()
			w.activity(Record{Event: "conversation_wake_stopped", Category: "decision", ThreadID: in.Thread, MessageID: wake.Parent, Status: "blocked", Reason: "wake_permission_budget_or_context"})
			continue
		}
		wake.State = "claimed"
		w.persistErr = w.saveRuntime()
		if w.persistErr != nil {
			return true
		}
		in.Mode = "proactive"
		in.PersonaOverride = w.config.GroupRules[in.Thread].Persona
		in.Directed = false
		in.QuoteServerID = ""
		in.QuoteSharedMessage = false
		in.MustReply = false
		in.TraceID = "wake-" + messageHash(wake.Parent + "|" + wake.Due.UTC().Format(time.RFC3339Nano))[:40]
		in.Text = "这是你先前安排的一次延时重新评估。结合本会话最新消息、已经发出的内容及关系记忆，判断是否值得自然补充、继续一句或安静。它不是必须发送的提醒，不要重复旧回答，不催促对方，不凭空自我介绍。没有新的补充价值就silent。"
		w.activity(Record{Event: "conversation_wake_started", Category: "decision", ThreadID: in.Thread, MessageID: in.TraceID, Status: "processing", Reason: "independent_conversation_wake"})
		w.respondCharacter(ctx, c, in)
		wake.State = "completed"
		if w.generation != nil {
			wake.State = "running"
		}
		w.persistErr = w.saveRuntime()
		return true
	}
	return false
}
