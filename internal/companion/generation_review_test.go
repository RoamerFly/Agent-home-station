package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunningRequestReviewIsNotUnsentReplyReplacement(t *testing.T) {
	model := &fakeModel{answer: `{"action":"replace","messages":["new reply"]}`}
	store, e := NewStore(t.TempDir())
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	if e != nil {
		t.Fatal(e)
	}
	engine, e := New(enabledConfig(), store, model, "test")
	if e != nil {
		t.Fatal(e)
	}
	in := Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "person", Text: "实际图像观察：早餐", IsGroup: true}
	updates := []ConversationEvent{{MessageID: "caption", Sender: "person", Text: "早餐", NewRequest: true}}
	if _, e = engine.ReviewGeneration(context.Background(), in, []string{"observe image"}, updates); e == nil {
		t.Fatal("a text-tail replacement could cancel a running media request")
	}
	var payload struct {
		Kind    string              `json:"kind"`
		Request string              `json:"original_request"`
		Events  []ConversationEvent `json:"new_events"`
	}
	if e = json.Unmarshal([]byte(model.turns[0]["content"]), &payload); e != nil || payload.Kind != "review_running_task" || payload.Request != in.Text || len(payload.Events) != 1 {
		t.Fatal("running request context missing", e)
	}
	// Existing unsent-text plans can still be rewritten when needed.
	if d, e := engine.ReviewPending(context.Background(), in, nil, []string{"old reply"}, updates); e != nil || d.Action != "replace" {
		t.Fatal("unsent text replacement regressed", e)
	}
}

func TestMediaCaptionReviewRetainsVerifiedAttachmentContext(t *testing.T) {
	m := &fakeModel{answer: `{"action":"continue","reason":"补充已发视频的介绍"}`}
	store, _ := NewStore(t.TempDir())
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	e, _ := New(enabledConfig(), store, m, "")
	_, err := e.ReviewMediaCaption(context.Background(), Input{Surf: true, Mode: "proactive", Text: "找个有趣的视频"}, nil, []string{"这个视频很有趣"}, nil, []DeliveredAttachment{{Kind: "video", Title: "合成短视频", State: "accepted"}})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Kind    string
		Media   []DeliveredAttachment `json:"delivered_attachments"`
		Pending []string              `json:"pending_messages"`
	}
	if json.Unmarshal([]byte(m.turns[0]["content"]), &payload) != nil || payload.Kind != "review_delivered_media_caption" || len(payload.Media) != 1 || payload.Media[0].State != "accepted" || len(payload.Pending) != 1 {
		t.Fatal("caption lost delivered media", m.turns)
	}
	if !strings.Contains(m.system, "附件已发不代表配文重复") {
		t.Fatal("missing caption review semantics")
	}
}
