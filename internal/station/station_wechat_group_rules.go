package station

import (
	"strings"
	"time"
	"unicode/utf8"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
)

type stationWechatGroupRule struct {
	Note           string    `json:"note,omitempty"`
	SummarizeSince time.Time `json:"summarize_since,omitzero"`
}

func validStationGroupRule(thread string, rule stationWechatGroupRule, replies, attachments map[string]time.Time) bool {
	parent := replies[thread]
	if !validStationGroup(thread) || parent.IsZero() || len(rule.Note) > 4000 || !utf8.ValidString(rule.Note) {
		return false
	}
	if rule.SummarizeSince.IsZero() {
		return true
	}
	media := attachments[thread]
	return !media.IsZero() && !rule.SummarizeSince.Before(media) && !rule.SummarizeSince.Before(parent)
}

func stationGroupRules(previous map[string]stationWechatGroupRule, requested *[]apphost.ConnectionGroupRule, replies, attachments map[string]time.Time, now time.Time) (map[string]stationWechatGroupRule, error) {
	result := make(map[string]stationWechatGroupRule)
	if requested == nil {
		for thread, rule := range previous {
			if attachments[thread].IsZero() {
				rule.SummarizeSince = time.Time{}
			}
			if validStationGroupRule(thread, rule, replies, attachments) && (rule.Note != "" || !rule.SummarizeSince.IsZero()) {
				result[thread] = rule
			}
		}
		return result, nil
	}
	if len(*requested) > 100 {
		return nil, apphost.ErrInvalidConnectionPolicy
	}
	seen := make(map[string]bool, len(*requested))
	for _, row := range *requested {
		rule := stationWechatGroupRule{Note: strings.TrimSpace(row.Note)}
		if row.SummarizeShares {
			rule.SummarizeSince = previous[row.ThreadID].SummarizeSince
			if rule.SummarizeSince.IsZero() {
				rule.SummarizeSince = now
			}
		}
		if seen[row.ThreadID] || !validStationGroupRule(row.ThreadID, rule, replies, attachments) {
			return nil, apphost.ErrInvalidConnectionPolicy
		}
		seen[row.ThreadID] = true
		if rule.Note != "" || !rule.SummarizeSince.IsZero() {
			result[row.ThreadID] = rule
		}
	}
	return result, nil
}

func (p stationWechatPolicy) groupShareAllowed(thread, timestamp string) bool {
	at, err := time.Parse(time.RFC3339Nano, timestamp)
	rule := p.GroupRules[thread]
	return err == nil && !rule.SummarizeSince.IsZero() && !p.Grants[thread].IsZero() &&
		!p.AttachmentGrants[thread].IsZero() && !at.Before(rule.SummarizeSince)
}

func (p stationWechatPolicy) groupShareCandidate(account string, m linuxnative.Message) bool {
	return m.AccountID == account && m.Direction == "incoming" && p.groupShareAllowed(m.ThreadID, m.CreatedAt) &&
		wechatapp.IsChannelsShare(m)
}
