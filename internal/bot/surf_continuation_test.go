package bot

import (
	"context"
	"encoding/json"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestDisabledSurfStopsLegacyWakeChainWithoutModelOrNewSend(t *testing.T) {
	m := &planReviewModel{answer: `{"action":"silent"}`}
	w, c, n, in := planWorker(t, m)
	c.Config.Conversation.AllowWake = true
	c.Config.Permissions.Proactive = true
	c.Config.ProactiveThreads = []string{in.Thread}
	w.config.SurfAllowed = func(string) bool { return false }
	in.Surf = true
	in.TraceID = "surf-synthetic"
	first := &conversationWake{Input: in, Parent: in.TraceID, Due: time.Now().Add(-2 * time.Minute), State: "completed"}
	legacy := in
	legacy.Surf = false
	legacy.TraceID = "wake-" + messageHash(first.Parent + "|" + first.Due.UTC().Format(time.RFC3339Nano))[:40]
	second := &conversationWake{Input: legacy, Parent: legacy.TraceID, Due: time.Now().Add(-time.Minute), State: "waiting"}
	w.runtime.Wakes = []*conversationWake{first, second}
	if !w.surfOrigin(legacy) {
		t.Fatal("legacy provenance lost")
	}
	if w.tickConversationWake(context.Background()) || second.State != "stopped" || m.calls != 0 || len(n.submissions) != 0 {
		t.Fatal("disabled surf woke model or sent", second.State, m.calls)
	}
	if _, err := w.sendText(context.Background(), legacy, 0, "不该发送"); err == nil || len(n.submissions) != 0 {
		t.Fatal("disabled legacy surf text sent")
	}
}

func TestEnabledSurfWakeKeepsProvenanceAndCannotInheritOwner(t *testing.T) {
	m := &planReviewModel{answer: `{"action":"silent"}`}
	w, c, _, in := planWorker(t, m)
	c.Config.Conversation.AllowWake = true
	c.Config.Permissions.Proactive = true
	c.Config.ProactiveThreads = []string{in.Thread}
	w.config.SurfAllowed = func(string) bool { return true }
	in.Surf = true
	in.TraceID = "surf-test"
	w.scheduleConversationWake(c, in, 30, false, "继续浏览")
	w.runtime.Wakes[0].Due = time.Now().Add(-time.Second)
	if !w.tickConversationWake(context.Background()) || m.calls != 1 {
		t.Fatal("allowed wake did not run")
	}
	var current struct {
		Surf   bool   `json:"surf"`
		Sender string `json:"sender"`
	}
	found := false
	for _, turn := range m.turns {
		if turn["role"] == "user" {
			var row map[string]any
			json.Unmarshal([]byte(turn["content"]), &row)
			if row["kind"] == "current_message" {
				json.Unmarshal([]byte(turn["content"]), &current)
				found = true
			}
		}
	}
	if !found || !current.Surf || current.Sender != "" {
		t.Fatal("wake lost surf identity or kept owner", m.turns)
	}
}

func TestCaptionReviewSeesAcceptedVideoAndKeepsTextForDelivery(t *testing.T) {
	m := &planReviewModel{answer: `{"action":"continue","reason":"给已发送的视频补介绍"}`}
	w, c, n, in := planWorker(t, m)
	c.Config.ShowAILabel = false
	in.Surf = true
	in.Mode = "proactive"
	w.config.SurfAllowed = func(string) bool { return true }
	w.runtime.Media = append(w.runtime.Media, &mediaDelivery{Trace: in.TraceID, Input: in, Kind: "video", Title: "合成视频", State: "accepted"})
	w.queueUtteranceFrom(c, in, companion.Decision{Messages: []string{"这段视频的介绍"}}, 0, true, []companion.ConversationEvent{{MessageID: "new", Text: "普通互聊"}})
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	var data struct {
		Kind  string
		Media []companion.DeliveredAttachment `json:"delivered_attachments"`
	}
	json.Unmarshal([]byte(m.turns[0]["content"]), &data)
	if data.Kind != "review_delivered_media_caption" || len(data.Media) != 1 || data.Media[0].State != "accepted" {
		t.Fatal("missing verified media context", data)
	}
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if len(n.submissions) != 1 || n.submissions[0].Text != "这段视频的介绍" {
		t.Fatal("caption lost", n.submissions)
	}
}
