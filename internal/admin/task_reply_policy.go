package admin

import (
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

// Task phase, not reply wording, controls whether a scheduled execution may speak.
func (m *Manager) taskReplyReady(in companion.Input, d companion.Decision) bool {
	t, ok := m.agent.lookupTask(in.TaskID, in.Account, "")
	if !ok {
		return false
	}
	if children := m.agent.crossThreadChildren(t); children.found {
		// Keep the task running until the reply plan is persisted and bound to
		// its receipt. A child that has already failed must not suppress the
		// response prepared in this turn.
		t.CrossThreadChildWait = true
		if _, err := m.agent.updateSnapshot(t); err != nil {
			return false
		}
		if t.DeliveryThread != "" && t.DeliveryThread != t.Thread {
			return d.Action == "silent"
		}
		return true
	}
	if t.DeliveryThread != "" && t.DeliveryThread != t.Thread {
		// This round still owns the source conversation. Delivery uses the
		// destination-bound tools and their own receipt ledger.
		return d.Action == "silent"
	}
	if t.State == "completed" || t.State == "needs_review" || t.WorkCompleted || len(d.MediaIDs) > 0 || d.ImageID != "" {
		return true
	}
	if t.State == "running" {
		t.State = "waiting"
		t.Next = time.Now().Add(bot.DeliveryRetryBackoff(t.Runs))
		t.LastResult = "执行任务尚未完成；保留工具结果，静默继续处理"
		t.stopRepeatedNoProgress("reply_before_completion")
		_, _ = m.agent.updateSnapshot(t)
	}
	return false
}
