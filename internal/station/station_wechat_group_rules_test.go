package station

import (
	"testing"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
)

func TestStationGroupSharePolicyNeedsCurrentScopedOptIn(t *testing.T) {
	const room = "synthetic-room@chatroom"
	now := time.Now().UTC()
	replies := map[string]time.Time{room: now.Add(-time.Minute)}
	attachments := map[string]time.Time{room: now.Add(-time.Minute)}
	want := []apphost.ConnectionGroupRule{{ThreadID: room, Note: " brief ", SummarizeShares: true}}
	rules, err := stationGroupRules(nil, &want, replies, attachments, now)
	if err != nil || rules[room].Note != "brief" || !rules[room].SummarizeSince.Equal(now) {
		t.Fatal(rules, err)
	}
	policy := stationWechatPolicy{Grants: replies, AttachmentGrants: attachments, GroupRules: rules}
	kind := 51
	m := linuxnative.Message{ID: "synthetic-card", ThreadID: room, AccountID: "one", Direction: "incoming", CreatedAt: now.Format(time.RFC3339Nano), Type: 49, AppMessageType: &kind}
	if !policy.groupShareCandidate("one", m) || policy.groupShareCandidate("two", m) {
		t.Fatal("account or opt-in boundary was lost")
	}
	m.CreatedAt = now.Add(-time.Second).Format(time.RFC3339Nano)
	if policy.groupShareCandidate("one", m) {
		t.Fatal("backlog predating opt-in was admitted")
	}
	m.CreatedAt = now.Format(time.RFC3339Nano)
	kind = 5
	if policy.groupShareCandidate("one", m) {
		t.Fatal("ordinary link treated as a supported video")
	}
	downgraded, err := stationGroupRules(rules, nil, replies, nil, now.Add(time.Second))
	if err != nil || !downgraded[room].SummarizeSince.IsZero() || downgraded[room].Note != "brief" {
		t.Fatal(downgraded, err)
	}
	resumed, err := stationGroupRules(downgraded, nil, replies, attachments, now.Add(2*time.Second))
	if err != nil || !resumed[room].SummarizeSince.IsZero() {
		t.Fatal("regrant silently enabled summary", resumed, err)
	}
}

func TestStationGroupReplySettingsPreserveLegacyAndAdvanceEpoch(t *testing.T) {
	const room = "synthetic-room@chatroom"
	now := time.Now().UTC().Truncate(time.Nanosecond)
	replies := map[string]time.Time{room: now.Add(-time.Minute)}
	previous := map[string]stationWechatGroupRule{room: {Mode: "all", Prefix: "问： ", Persona: "群聊人格", ForceReplyWhenMentioned: true, ReplySince: now.Add(-time.Second)}}

	// A caller from before reply_settings existed must not erase it.
	legacy := []apphost.ConnectionGroupRule{{ThreadID: room, Note: "旧客户端"}}
	rules, err := stationGroupRules(previous, &legacy, replies, nil, now)
	if err != nil || rules[room].replyRule() != previous[room].replyRule() || !rules[room].ReplySince.Equal(previous[room].ReplySince) {
		t.Fatal(rules, err)
	}
	unchanged := []apphost.ConnectionGroupRule{{ThreadID: room, ReplySettings: &apphost.ConnectionGroupReplySettings{Mode: "all", Prefix: "问： ", Persona: "群聊人格", ForceReplyWhenMentioned: true}}}
	rules, err = stationGroupRules(rules, &unchanged, replies, nil, now)
	if err != nil || !rules[room].ReplySince.Equal(previous[room].ReplySince) {
		t.Fatal("unchanged settings advanced reply epoch", rules, err)
	}

	// An explicit default replaces non-default values and starts a fresh epoch.
	reset := []apphost.ConnectionGroupRule{{ThreadID: room, ReplySettings: &apphost.ConnectionGroupReplySettings{Mode: "mention"}}}
	rules, err = stationGroupRules(rules, &reset, replies, nil, now)
	if err != nil || rules[room].replyRule().Mode != "mention" || rules[room].hasReplySettings() || !rules[room].ReplySince.Equal(now) {
		t.Fatal(rules, err)
	}

	prefix := "问： "
	updated := []apphost.ConnectionGroupRule{{ThreadID: room, ReplySettings: &apphost.ConnectionGroupReplySettings{Mode: "all", Prefix: prefix, Persona: "新人格"}}}
	rules, err = stationGroupRules(rules, &updated, replies, nil, now.Add(time.Second))
	if err != nil || rules[room].Prefix != prefix || rules[room].Persona != "新人格" || rules[room].Mode != "all" || !rules[room].ReplySince.Equal(now.Add(time.Second)) {
		t.Fatal(rules, err)
	}

	invalid := []apphost.ConnectionGroupRule{{ThreadID: room, ReplySettings: &apphost.ConnectionGroupReplySettings{Mode: "social"}}}
	if _, err := stationGroupRules(rules, &invalid, replies, nil, now); err != apphost.ErrInvalidConnectionPolicy {
		t.Fatal("social mode was accepted", err)
	}
}
