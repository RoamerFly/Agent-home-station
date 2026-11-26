package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestMentionContextMayUseOlderHistoryWithoutReactivatingOldRequests(t *testing.T) {
	const room = "room@chatroom"
	w, c, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		t.Fatal("reading history invoked the model")
		return "", nil
	}))
	w.source = &fakeSource{messages: map[string]linuxnative.Message{}}
	w.allowed[room] = true
	w.config.GroupRules = map[string]GroupRule{room: {Mode: "mention"}}
	w.config.AuthorizeReply = func(companion.Input) bool { return false }
	w.config.MentionHistoryAllowed = func(context.Context, linuxnative.Message) bool { return true }
	m := fixtureMessage("old-message", room, "设置前的群聊", "incoming")
	m.SenderID = "member"
	m.CreatedAt = time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano)
	if w.authorizedMessage(m) {
		t.Fatal("old message unexpectedly became a new reply trigger")
	}
	if !w.mentionHistoryVisible(t.Context(), m, room) {
		t.Fatal("reply activation epoch hid older authorized context")
	}
	m.ServerID = "server-old-message"
	w.source.(*fakeSource).messages[m.ID] = m
	w.runtime.Inbox = append(w.runtime.Inbox, inboxItem{Event: fixtureEvent("1", m.ID, room, "incoming", "remote"), Phase: "received", ReceivedAt: time.Now().UTC(), RecallGeneration: c.Store.NativeRecallGeneration(m.AccountID, room)})
	if err := w.drainInbox(t.Context()); err != nil {
		t.Fatal(err)
	}
	if w.runtime.Inbox[0].Phase != "skipped" {
		t.Fatalf("old message reactivated: %s", w.runtime.Inbox[0].Phase)
	}
	scope, err := c.Store.Read(companion.ScopeID("wechat", m.AccountID, room), c.Config, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, turn := range scope.Messages {
		found = found || turn.TraceID == m.ID
	}
	if !found {
		t.Fatal("queued old receipt was lost from future compression")
	}
}

func TestMentionDormancyCollectionAndOneObservationDecision(t *testing.T) {
	const room = "room@chatroom"
	var payloads []string
	w, c, native := backgroundWorker(t, generationModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
		payloads = append(payloads, turns[len(turns)-1]["content"])
		return `{"action":"silent","messages":[],"reason":"无需回应"}`, nil
	}))
	quiet := false
	c.Config.Permissions.Proactive = true
	c.Config.ProactiveThreads = []string{room}
	w.config.GroupRules = map[string]GroupRule{room: {Mode: "mention"}}
	source := &fakeSource{messages: map[string]linuxnative.Message{}}
	w.source = source
	var calls int
	w.config.Analyzer = sourceCheckingAnalyzer(func(context.Context, linuxnative.Message) (string, error) { calls++; return "unexpected", nil })
	w.config.PreferredGroupShare = func(m linuxnative.Message) bool { return true }
	w.config.ReadPreferredGroupShare = nil
	var records []Record
	w.config.Record = func(r Record) { records = append(records, r) }
	receive := func(id, sender, body string, kind int, at time.Time, directed bool) {
		m := fixtureMessage(id, room, body, "incoming")
		m.ServerID = "server-" + id
		m.CreatedAt, m.SenderID, m.Type = at.UTC().Format(time.RFC3339Nano), sender, kind
		m.Mentions.State = "known"
		if directed {
			yes := true
			m.Mentions.Self = &yes
		} else {
			m.Mentions.Self = &quiet
		}
		source.messages[id] = m
		w.runtime.Inbox = append(w.runtime.Inbox, inboxItem{Event: fixtureEvent(fmt.Sprint(len(w.runtime.Inbox)+1), id, room, "incoming", "remote"), Phase: "received", Updated: at, ReceivedAt: at})
	}
	drain := func() {
		t.Helper()
		if err := w.drainInbox(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	receive("before", "bob", "午后的群消息", 1, now.Add(-4*time.Second), false)
	receive("older", "bob", "这是更早的消息", 1, now.Add(-20*time.Second), false)
	receive("idle-media", "bob", "", 43, now.Add(-3*time.Second), false)
	drain()
	w.tickCharacter(t.Context())
	if len(records) != 0 || len(payloads) != 0 || calls != 0 {
		t.Fatalf("idle events=%+v calls=%d model=%d", records, calls, len(payloads))
	}
	receive("anchor", "alice", "@机器人 这个时间合适吗", 1, now, true)
	receive("after", "bob", "会后时间也行", 1, now.Add(time.Millisecond), false)
	drain()
	anchor := w.mentionItem(room, "anchor")
	if len(payloads) != 0 || !anchor.AggregateUntil.Equal(now.Add(mentionCollection)) {
		t.Fatalf("unexpected initial deadline %v calls=%d", anchor.AggregateUntil, len(payloads))
	}
	anchor.AggregateUntil = now.Add(-time.Millisecond)
	drain()
	if len(payloads) != 1 || !strings.Contains(payloads[0], "午后的群消息") || !strings.Contains(payloads[0], "会后时间也行") || !strings.Contains(payloads[0], source.messages["before"].CreatedAt) || len(native.submissions) != 0 || calls != 0 {
		t.Fatalf("missing verified window or unexpected send/read: %v", payloads)
	}
	var first struct {
		Text string `json:"text"`
		Rows []struct {
			ID string `json:"message_id"`
			At string `json:"at"`
		} `json:"unread_messages"`
	}
	if err := json.Unmarshal([]byte(payloads[0]), &first); err != nil || first.Text != source.messages["anchor"].Body || len(first.Rows) != 4 {
		t.Fatalf("anchor or context lost: %+v", first)
	}
	for i, want := range []string{"older", "before", "idle-media", "after"} {
		if first.Rows[i].ID != want {
			t.Fatalf("window not ordered by spoken time: %+v", first.Rows)
		}
	}
	if anchor.Mention.ObservingFrom.IsZero() {
		t.Fatal("silent decision did not open observation")
	}
	start := time.Now().UTC().Add(-31 * time.Second)
	anchor.Mention.ObservingFrom = start
	receive("obs-one", "bob", "附近在下雨", 1, start.Add(5*time.Second), false)
	receive("obs-two", "carol", "我带伞了", 1, start.Add(6*time.Second), false)
	receive("expired", "bob", "已经回家", 1, start.Add(31*time.Second), false)
	drain()
	drain()
	if len(payloads) != 2 || !strings.Contains(payloads[1], "附近在下雨") || !strings.Contains(payloads[1], "我带伞了") || !strings.Contains(payloads[1], "选择 silent") {
		t.Fatalf("observation batch incorrect: %v", payloads)
	}
	if strings.Contains(payloads[1], "已经回家") {
		t.Fatal("post-window message entered observation")
	}
	if !w.mentionItem(room, "obs-one").Mention.ObservingFrom.IsZero() || w.mentionItem(room, "expired").Phase != "skipped" || len(native.submissions) != 0 || calls != 0 {
		t.Fatal("unrelated chatter prolonged observation")
	}
	var packet map[string]any
	if err := json.Unmarshal([]byte(payloads[1]), &packet); err != nil || packet["directed"] != false || packet["must_reply"] != false {
		t.Fatalf("observation gained fabricated authority: %v", packet)
	}
}

func TestMentionObservationWaitsForFinalAcceptedReply(t *testing.T) {
	const room = "room@chatroom"
	w, _, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		t.Fatal("observation timer called model")
		return "", nil
	}))
	w.config.GroupRules = map[string]GroupRule{room: {Mode: "mention"}}
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", "request", room, "incoming", "remote"), Mention: &mentionReceipt{Kind: mentionTrigger, Sender: "alice"}}}
	plan := &utterancePlan{State: "queued"}
	plan.Input.Thread, plan.Input.TraceID = room, "request"
	w.runtime.Plans = []*utterancePlan{plan}
	sent := Record{Event: "message_sent", ThreadID: room, MessageID: "request", Origin: "bot", Status: "accepted"}
	w.startMentionObservation(sent)
	if !w.runtime.Inbox[0].Mention.ObservingFrom.IsZero() {
		t.Fatal("first accepted bubble opened the observation too early")
	}
	plan.State = "accepted"
	sent.Status = "unknown"
	w.startMentionObservation(sent)
	if !w.runtime.Inbox[0].Mention.ObservingFrom.IsZero() {
		t.Fatal("ambiguous send opened observation")
	}
	sent.Status = "accepted"
	w.startMentionObservation(sent)
	began := w.runtime.Inbox[0].Mention.ObservingFrom
	w.startMentionObservation(sent)
	if began.IsZero() || !began.Equal(w.runtime.Inbox[0].Mention.ObservingFrom) {
		t.Fatal("final receipt did not open exactly one observation")
	}
}
