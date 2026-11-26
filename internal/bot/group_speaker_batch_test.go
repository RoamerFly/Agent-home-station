package bot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/linuxnative"
)

func TestLiveGroupWindowKeepsOtherSpeakersImageAndFollowupForSecondDecision(t *testing.T) {
	const room = "room@chatroom"
	var batches [][]json.RawMessage
	w, c, native := backgroundWorker(t, generationModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
		var payload struct {
			Unread []json.RawMessage `json:"unread_messages"`
		}
		if err := json.Unmarshal([]byte(turns[len(turns)-1]["content"]), &payload); err != nil {
			t.Fatal(err)
		}
		batches = append(batches, payload.Unread)
		if len(batches) == 1 {
			return `{"action":"reply","messages":["first reply"]}`, nil
		}
		return `{"action":"reply","messages":["image reply"]}`, nil
	}))
	c.Config.Permissions.ObserveGroups, c.Config.Permissions.AutonomousGroups = true, true
	w.config.GroupRules = map[string]GroupRule{room: {Mode: "social"}}
	w.config.Options.ReadImages = true
	w.config.Analyzer = sourceCheckingAnalyzer(func(_ context.Context, m linuxnative.Message) (string, error) {
		return "asset_id=asset-system-image; source_message_id=" + m.ID + "; 请调用 artifact_view_image 查看原图", nil
	})
	now := time.Now().UTC()
	source := &fakeSource{messages: map[string]linuxnative.Message{}}
	for i, spec := range []struct {
		id, sender, text string
		kind             int
	}{
		{"search-retry", "member-a", "重试先前的搜索", 1},
		{"system-image", "member-b", "", 3},
		{"which-one", "member-b", "which one", 1},
	} {
		m := fixtureMessage(spec.id, room, spec.text, "incoming")
		m.Type, m.SenderID, m.CreatedAt = spec.kind, spec.sender, now.Add(time.Duration(i-5)*time.Second).Format(time.RFC3339)
		source.messages[m.ID] = m
		w.runtime.Inbox = append(w.runtime.Inbox, inboxItem{Event: fixtureEvent(spec.id, spec.id, room, "incoming", "remote"), Phase: "received", Updated: now, AggregateUntil: now.Add(-time.Second)})
	}
	w.source = source
	w.enableGenerations()
	defer w.closeGenerations()
	if err := w.drainInbox(t.Context()); err != nil {
		t.Fatal(err)
	}
	pumpGeneration(t, w, func() bool { return w.generation.occupied() == 0 })
	if len(batches) != 1 || len(batches[0]) != 1 || !strings.Contains(string(batches[0][0]), `"message_id":"search-retry"`) || !w.runtime.Inbox[1].GroupDeferred || !w.runtime.Inbox[2].GroupDeferred {
		t.Fatalf("first speaker swallowed the other request: batches=%v inbox=%+v", batches, w.runtime.Inbox)
	}
	readyPlans(w)
	w.reconcileUtterancePlans(t.Context())
	if len(native.submissions) != 1 || w.runtime.Inbox[1].Phase != "received" || w.runtime.Inbox[2].Phase != "received" {
		t.Fatalf("parent reply settled another speaker: sends=%d inbox=%+v", len(native.submissions), w.runtime.Inbox)
	}
	if err := w.drainInbox(t.Context()); err != nil {
		t.Fatal(err)
	}
	pumpGeneration(t, w, func() bool { return w.generation.occupied() == 0 })
	if len(batches) != 2 || len(batches[1]) != 2 || !strings.Contains(string(batches[1][0]), `"message_id":"system-image"`) || !strings.Contains(string(batches[1][1]), `"message_id":"which-one"`) || !strings.Contains(string(batches[1][0]), "asset_id=asset-system-image") || !strings.Contains(string(batches[1][0]), "source_message_id=system-image") || !strings.Contains(string(batches[1][1]), "which one") || strings.Contains(string(batches[1][0]), "重试先前的搜索") || strings.Contains(string(batches[1][1]), "重试先前的搜索") {
		t.Fatalf("image and followup were not read together: %+v", batches)
	}
	readyPlans(w)
	w.reconcileUtterancePlans(t.Context())
	if len(native.submissions) != 2 || w.runtime.Inbox[1].Phase != "replied" || w.runtime.Inbox[2].Phase != "replied" {
		t.Fatalf("second reply was not delivered exactly once: sends=%d inbox=%+v", len(native.submissions), w.runtime.Inbox)
	}
}
