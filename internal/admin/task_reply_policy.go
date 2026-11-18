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
		_, _ = m.agent.updateSnapshot(t)
	}
	return false
}
