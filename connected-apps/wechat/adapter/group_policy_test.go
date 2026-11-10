package adapter

import (
	"testing"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestSocialParticipationOnlyFollowsGrantedGroupRules(t *testing.T) {
	now := time.Now().UTC()
	policy := GroupPolicy{
		Replies: map[string]time.Time{"social@chatroom": now, "mention@chatroom": now, "friend": now, "zero@chatroom": {}},
		Rules: map[string]GroupRule{
			"social@chatroom": {Mode: "social"}, "foreign@chatroom": {Mode: "social"},
			"friend": {Mode: "social"}, "zero@chatroom": {Mode: "social"},
		},
	}
	config := companion.Defaults()
	threads, rules := policy.Participation(&config)
	if len(threads) != 3 || len(rules) != 2 || rules["social@chatroom"].Mode != "social" || rules["mention@chatroom"].Mode != "mention" ||
		!config.Permissions.ObserveGroups || !config.Permissions.AutonomousGroups {
		t.Fatal("explicit social scope was not reflected in engine permissions", threads, rules, config.Permissions)
	}
	delete(policy.Replies, "social@chatroom")
	_, rules = policy.Participation(&config)
	if len(rules) != 1 || config.Permissions.ObserveGroups || config.Permissions.AutonomousGroups {
		t.Fatal("ungranted, private or zero-epoch rules retained social permission", rules, config.Permissions)
	}
}

func TestGroupSharePolicyNeedsCurrentScopedOptIn(t *testing.T) {
	const room = "synthetic-room@chatroom"
	now := time.Now().UTC()
	replies := map[string]time.Time{room: now.Add(-time.Minute)}
	attachments := map[string]time.Time{room: now.Add(-time.Minute)}
	want := []apphost.ConnectionGroupRule{{ThreadID: room, Note: " brief ", SummarizeShares: true}}
	rules, err := UpdateGroupRules(nil, &want, replies, attachments, now)
	if err != nil || rules[room].Note != "brief" || !rules[room].SummarizeSince.Equal(now) {
		t.Fatal(rules, err)
	}
	policy := GroupPolicy{Replies: replies, Attachments: attachments, Rules: rules}
	kind := 51
	m := linuxnative.Message{ID: "synthetic-card", ThreadID: room, AccountID: "one", Direction: "incoming", CreatedAt: now.Format(time.RFC3339Nano), Type: 49, AppMessageType: &kind}
	if !policy.ShareCandidate("one", m) || policy.ShareCandidate("two", m) {
		t.Fatal("account or opt-in boundary was lost")
	}
	m.CreatedAt = now.Add(-time.Second).Format(time.RFC3339Nano)
	if policy.ShareCandidate("one", m) {
		t.Fatal("backlog predating opt-in was admitted")
	}
	m.CreatedAt = now.Format(time.RFC3339Nano)
	kind = 5
	if policy.ShareCandidate("one", m) {
		t.Fatal("ordinary link treated as a supported video")
	}
	if (GroupPolicy{Replies: replies, Attachments: map[string]time.Time{room: now.Add(time.Second)}, Rules: rules}).
		AllowsShare(room, now.Add(2*time.Second).Format(time.RFC3339Nano)) {
		t.Fatal("a new attachment grant reused an old summary opt-in")
	}
	downgraded, err := UpdateGroupRules(rules, nil, replies, nil, now.Add(time.Second))
	if err != nil || !downgraded[room].SummarizeSince.IsZero() || downgraded[room].Note != "brief" {
		t.Fatal(downgraded, err)
	}
	resumed, err := UpdateGroupRules(downgraded, nil, replies, attachments, now.Add(2*time.Second))
	if err != nil || !resumed[room].SummarizeSince.IsZero() {
		t.Fatal("regrant silently enabled summary", resumed, err)
	}
}

func TestGroupReplySettingsPreserveLegacyAndAdvanceEpoch(t *testing.T) {
	const room = "synthetic-room@chatroom"
	now := time.Now().UTC().Truncate(time.Nanosecond)
	replies := map[string]time.Time{room: now.Add(-time.Minute)}
	previous := map[string]GroupRule{room: {Mode: "all", Prefix: "问： ", Persona: "群聊人格", ForceReplyWhenMentioned: true, ReplySince: now.Add(-time.Second)}}

	// A caller from before reply_settings existed must not erase it.
	legacy := []apphost.ConnectionGroupRule{{ThreadID: room, Note: "旧客户端"}}
	rules, err := UpdateGroupRules(previous, &legacy, replies, nil, now)
	if err != nil || rules[room].replyRule() != previous[room].replyRule() || !rules[room].ReplySince.Equal(previous[room].ReplySince) {
		t.Fatal(rules, err)
	}
	unchanged := []apphost.ConnectionGroupRule{{ThreadID: room, ReplySettings: &apphost.ConnectionGroupReplySettings{Mode: "all", Prefix: "问： ", Persona: "群聊人格", ForceReplyWhenMentioned: true}}}
	rules, err = UpdateGroupRules(rules, &unchanged, replies, nil, now)
	if err != nil || !rules[room].ReplySince.Equal(previous[room].ReplySince) {
		t.Fatal("unchanged settings advanced reply epoch", rules, err)
	}

	// An explicit default replaces non-default values and starts a fresh epoch.
	reset := []apphost.ConnectionGroupRule{{ThreadID: room, ReplySettings: &apphost.ConnectionGroupReplySettings{Mode: "mention"}}}
	rules, err = UpdateGroupRules(rules, &reset, replies, nil, now)
	if err != nil || rules[room].replyRule().Mode != "mention" || rules[room].HasReplySettings() || !rules[room].ReplySince.Equal(now) {
		t.Fatal(rules, err)
	}

	prefix := "问： "
	updated := []apphost.ConnectionGroupRule{{ThreadID: room, ReplySettings: &apphost.ConnectionGroupReplySettings{Mode: "all", Prefix: prefix, Persona: "新人格"}}}
	rules, err = UpdateGroupRules(rules, &updated, replies, nil, now.Add(time.Second))
	if err != nil || rules[room].Prefix != prefix || rules[room].Persona != "新人格" || rules[room].Mode != "all" || !rules[room].ReplySince.Equal(now.Add(time.Second)) {
		t.Fatal(rules, err)
	}

	social := []apphost.ConnectionGroupRule{{ThreadID: room, ReplySettings: &apphost.ConnectionGroupReplySettings{Mode: "social"}}}
	rules, err = UpdateGroupRules(rules, &social, replies, nil, now.Add(2*time.Second))
	if err != nil || rules[room].Mode != "social" || !rules[room].HasReplySettings() || !rules[room].ReplySince.Equal(now.Add(2*time.Second)) {
		t.Fatal("social rule or authorization epoch was lost", rules, err)
	}
	preserved, err := UpdateGroupRules(rules, nil, replies, nil, now.Add(3*time.Second))
	if err != nil || preserved[room] != rules[room] {
		t.Fatal("an older caller erased social participation", preserved, err)
	}
	revoked, err := UpdateGroupRules(rules, nil, nil, nil, now.Add(4*time.Second))
	if err != nil || len(revoked) != 0 {
		t.Fatal("social participation survived group revocation", revoked, err)
	}
	invalid := []apphost.ConnectionGroupRule{{ThreadID: room, ReplySettings: &apphost.ConnectionGroupReplySettings{Mode: "unsupported"}}}
	if _, err := UpdateGroupRules(rules, &invalid, replies, nil, now); err != apphost.ErrInvalidConnectionPolicy {
		t.Fatal("unknown mode was accepted", err)
	}
}

func TestGroupReplyAndNoteStayWithinCurrentScope(t *testing.T) {
	const room = "synthetic-room@chatroom"
	now := time.Now().UTC()
	p := GroupPolicy{Replies: map[string]time.Time{room: now.Add(-time.Minute), "friend": now},
		Rules: map[string]GroupRule{room: {Note: "synthetic group note", ReplySince: now}, "friend": {Note: "unexpected"}}}
	in := companion.Input{Channel: ID, Account: "one", Thread: room, MessageTime: now.Add(-time.Second).Format(time.RFC3339Nano)}
	if p.AllowsReply("one", in) {
		t.Fatal("a queued reply survived a rule change")
	}
	in.MessageTime = now.Format(time.RFC3339Nano)
	if !p.AllowsReply("one", in) || p.AllowsReply("other", in) {
		t.Fatal("reply authorization was not bound to the current account and epoch")
	}
	if note, ok := p.ConversationNote("one", in); !ok || note != "synthetic group note" {
		t.Fatal("the current conversation lost its group note")
	}
	if _, ok := p.ConversationNote("other", in); ok {
		t.Fatal("a different account obtained the group note")
	}
	in.Preview = true
	if _, ok := p.ConversationNote("one", in); ok {
		t.Fatal("a preview obtained the group note")
	}
	in.Preview, in.Thread = false, "friend"
	if _, ok := p.ConversationNote("one", in); ok {
		t.Fatal("a private conversation obtained a group note")
	}
}
