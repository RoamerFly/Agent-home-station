package admin

import (
	"context"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/linuxnative"
)

// The legacy console has no separate history-grant epoch. Recheck its current
// reply rule and memory permissions for every candidate. The bot checks
// source quarantine and recall generations against its memory store.
func (m *Manager) mentionHistoryAllowed(ctx context.Context, candidate linuxnative.Message) bool {
	if ctx.Err() != nil || candidate.AccountID != m.native.Account() ||
		!wechatapp.ValidGroup(candidate.ThreadID) || candidate.ID == "" {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, candidate.CreatedAt); err != nil {
		return false
	}
	c := m.Config().Config
	if !c.Companion.Permissions.Remember {
		return false
	}
	for _, rule := range c.GroupRules {
		if rule.ID != candidate.ThreadID {
			continue
		}
		return rule.Mode == "mention"
	}
	return false
}
