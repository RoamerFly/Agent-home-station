package bot

import (
	"context"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestSharePreferenceConsidersCardsWithoutInventingMention(t *testing.T) {
	w, e, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		return `{"action":"silent","messages":[]}`, nil
	}))
	room := "synthetic@chatroom"
	w.allowed[room] = true
	w.config.GroupRules = map[string]GroupRule{room: {Mode: "mention"}}
	w.config.Analyzer = unavailableShareAnalyzer{}
	w.config.Options.ReadVideos = true
	e.Config.Permissions.ObserveGroups = true
	e.Config.Permissions.AutonomousGroups = true
	e.Config.Permissions.ReplyText = true
	input := companion.Input{Channel: w.config.Channel, Account: w.source.Account(), Thread: room, Sender: "a", Text: "hello", TraceID: "seed"}
	if err := e.Store.Observe(input, e.Config, time.Now()); err != nil {
		t.Fatal(err)
	}
	m := fixtureMessage("card", room, "", "incoming")
	m.Type = 49
	m.Link = &linuxnative.LinkCard{URL: "https://www.kuaishou.com/short-video/3xsynthetic12345"}
	if w.considerPreferredShare(m) {
		t.Fatal("empty preferences changed trigger")
	}
	if err := e.Store.SetPreferences(companion.ScopeID(input.Channel, input.Account, room), "分享默认总结后发送", 0); err != nil {
		t.Fatal(err)
	}
	if !w.considerPreferredShare(m) {
		t.Fatal("share not considered")
	}
	channels := m
	subtype := 51
	channels.AppMessageType = &subtype
	channels.Link = nil
	if !w.considerPreferredShare(channels) {
		t.Fatal("native Channels identity without parsed card ignored")
	}
	quoted := m
	subtypeQuote := 57
	quoted.AppMessageType = &subtypeQuote
	if w.considerPreferredShare(quoted) {
		t.Fatal("quoted text was treated as a new share")
	}
	working := m
	working.Type = 1
	working.Body = "分享内容"
	if _, ok := w.promptWithSharePreference(working, time.Now(), true); !ok {
		t.Fatal("authorized share blocked by mention mode")
	}
	if w.addressed(working) {
		t.Fatal("fabricated mention")
	}
	working.CreatedAt = time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	if _, ok := w.promptWithSharePreference(working, time.Now(), true); ok {
		t.Fatal("stale card bypassed age")
	}
	e.Config.Permissions.AutonomousGroups = false
	if w.considerPreferredShare(m) {
		t.Fatal("preference bypassed autonomous permission")
	}
	e.Config.Permissions.AutonomousGroups = true
	m.ThreadID = "other@chatroom"
	if w.considerPreferredShare(m) {
		t.Fatal("cross room preference")
	}
}
