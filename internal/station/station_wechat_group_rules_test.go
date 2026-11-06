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
