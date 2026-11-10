package adapter

import (
	"strings"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
)

type GroupRule struct {
	Note                    string    `json:"note,omitempty"`
	SummarizeSince          time.Time `json:"summarize_since,omitzero"`
	Mode                    string    `json:"mode,omitempty"`
	Prefix                  string    `json:"prefix,omitempty"`
	Persona                 string    `json:"persona,omitempty"`
	ForceReplyWhenMentioned bool      `json:"force_reply_when_mentioned,omitempty"`
	// ReplySince is an authorization epoch for replies. It prevents inputs
	// queued before an explicit reply-policy change from being revived later.
	ReplySince time.Time `json:"reply_since,omitzero"`
}

// GroupPolicy is a read-only snapshot of the host's grants and WeChat rules
// for one worker run. The host fences the run before changing its grants.
type GroupPolicy struct {
	Replies     map[string]time.Time
	Attachments map[string]time.Time
	Rules       map[string]GroupRule
}

// Participation derives engine permissions only from currently granted
// groups. Bot rules retain each group's own trigger mode within that engine.
func (p GroupPolicy) Participation(config *companion.Config) ([]string, map[string]bot.GroupRule) {
	threads := make([]string, 0, len(p.Replies))
	rules := make(map[string]bot.GroupRule)
	config.Permissions.ObserveGroups, config.Permissions.AutonomousGroups = false, false
	for thread, granted := range p.Replies {
		if granted.IsZero() {
			continue
		}
		threads = append(threads, thread)
		if ValidGroup(thread) {
			rules[thread] = p.Rules[thread].replyRule()
			if rules[thread].Mode == "social" {
				config.Permissions.ObserveGroups, config.Permissions.AutonomousGroups = true, true
			}
		}
	}
	return threads, rules
}

func (rule GroupRule) replyRule() bot.GroupRule {
	mode := rule.Mode
	if mode == "" {
		mode = "mention"
	}
	return bot.GroupRule{Mode: mode, Prefix: rule.Prefix, Persona: rule.Persona, ForceReplyWhenMentioned: rule.ForceReplyWhenMentioned}
}

func (rule GroupRule) HasReplySettings() bool {
	return rule.Mode == "all" || rule.Mode == "social" || rule.Prefix != "" || rule.Persona != "" || rule.ForceReplyWhenMentioned
}

func validGroupReplyValues(mode, prefix, persona string) bool {
	if mode != "" && mode != "mention" && mode != "all" && mode != "social" {
		return false
	}
	return len(prefix) <= 64 && utf8.ValidString(prefix) &&
		!strings.ContainsAny(prefix, "\r\n\x00") &&
		len(persona) <= 4000 && utf8.ValidString(persona)
}

func ValidGroupReplySettings(settings apphost.ConnectionGroupReplySettings) bool {
	return validGroupReplyValues(settings.Mode, settings.Prefix, settings.Persona)
}

// SetReplySettings validates and normalizes a requested reply policy. Empty
// and "mention" are both persisted as the canonical default empty Mode. It
// advances ReplySince only when the effective bot rule changes, so callers can
// use it during a migration without reviving previously queued messages.
func (rule *GroupRule) SetReplySettings(settings apphost.ConnectionGroupReplySettings, now time.Time) error {
	if !ValidGroupReplySettings(settings) {
		return apphost.ErrInvalidConnectionPolicy
	}
	previous := rule.replyRule()
	rule.Mode, rule.Prefix, rule.Persona = "", settings.Prefix, settings.Persona
	rule.ForceReplyWhenMentioned = settings.ForceReplyWhenMentioned
	if settings.Mode == "all" || settings.Mode == "social" {
		rule.Mode = settings.Mode
	}
	if rule.replyRule() != previous {
		rule.ReplySince = now
	}
	return nil
}

func ValidGroupRule(thread string, rule GroupRule, replies, attachments map[string]time.Time) bool {
	parent := replies[thread]
	if !ValidGroup(thread) || parent.IsZero() || len(rule.Note) > 4000 || !utf8.ValidString(rule.Note) ||
		!validGroupReplyValues(rule.Mode, rule.Prefix, rule.Persona) || (!rule.ReplySince.IsZero() && rule.ReplySince.Before(parent)) {
		return false
	}
	if rule.SummarizeSince.IsZero() {
		return true
	}
	media := attachments[thread]
	return !media.IsZero() && !rule.SummarizeSince.Before(media) && !rule.SummarizeSince.Before(parent)
}

func UpdateGroupRules(previous map[string]GroupRule, requested *[]apphost.ConnectionGroupRule, replies, attachments map[string]time.Time, now time.Time) (map[string]GroupRule, error) {
	result := make(map[string]GroupRule)
	if requested == nil {
		for thread, rule := range previous {
			if attachments[thread].IsZero() {
				rule.SummarizeSince = time.Time{}
			}
			if ValidGroupRule(thread, rule, replies, attachments) && (rule.Note != "" || !rule.SummarizeSince.IsZero() || rule.HasReplySettings() || !rule.ReplySince.IsZero()) {
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
		// Keep existing reply settings when an older client omits the field.
		rule := previous[row.ThreadID]
		rule.Note = strings.TrimSpace(row.Note)
		if row.ReplySettings != nil {
			if err := rule.SetReplySettings(*row.ReplySettings, now); err != nil {
				return nil, err
			}
		}
		if row.SummarizeShares {
			if rule.SummarizeSince.IsZero() {
				rule.SummarizeSince = now
			}
		} else {
			rule.SummarizeSince = time.Time{}
		}
		if seen[row.ThreadID] || !ValidGroupRule(row.ThreadID, rule, replies, attachments) {
			return nil, apphost.ErrInvalidConnectionPolicy
		}
		seen[row.ThreadID] = true
		if rule.Note != "" || !rule.SummarizeSince.IsZero() || rule.HasReplySettings() || !rule.ReplySince.IsZero() {
			result[row.ThreadID] = rule
		}
	}
	return result, nil
}

// AllowsShare limits automatic summaries to eligible messages after opt-in
// and the current reply and attachment grants.
func (p GroupPolicy) AllowsShare(thread, timestamp string) bool {
	at, err := time.Parse(time.RFC3339Nano, timestamp)
	since, reply, media := p.Rules[thread].SummarizeSince, p.Replies[thread], p.Attachments[thread]
	return err == nil && ValidGroup(thread) && !since.IsZero() && !reply.IsZero() && !media.IsZero() &&
		!at.Before(since) && !since.Before(reply) && !since.Before(media)
}

func (p GroupPolicy) ShareCandidate(account string, m linuxnative.Message) bool {
	return m.AccountID == account && m.Direction == "incoming" && p.AllowsShare(m.ThreadID, m.CreatedAt) &&
		IsChannelsShare(m)
}

func (p GroupPolicy) AllowsReply(account string, in companion.Input) bool {
	granted := p.Replies[in.Thread]
	if in.Account != account || granted.IsZero() || (in.GroupShareSummary && !p.AllowsShare(in.Thread, in.MessageTime)) {
		return false
	}
	if since := p.Rules[in.Thread].ReplySince; since.After(granted) {
		granted = since
	}
	when, err := time.Parse(time.RFC3339Nano, in.MessageTime)
	return err == nil && !when.Before(granted)
}

func (p GroupPolicy) ConversationNote(account string, in companion.Input) (string, bool) {
	if in.Preview || in.Channel != ID || in.Account != account || !ValidGroup(in.Thread) || p.Replies[in.Thread].IsZero() {
		return "", false
	}
	return p.Rules[in.Thread].Note, true
}

func (rule GroupRule) View(thread string) apphost.ConnectionGroupRule {
	view := apphost.ConnectionGroupRule{ThreadID: thread, Note: rule.Note, SummarizeShares: !rule.SummarizeSince.IsZero()}
	if rule.HasReplySettings() {
		reply := rule.replyRule()
		view.ReplySettings = &apphost.ConnectionGroupReplySettings{
			Mode: reply.Mode, Prefix: reply.Prefix, Persona: reply.Persona, ForceReplyWhenMentioned: reply.ForceReplyWhenMentioned,
		}
	}
	return view
}

func ValidGroup(id string) bool {
	return identity.ValidPathID(id) && strings.HasSuffix(id, "@chatroom") && id != "@chatroom"
}
