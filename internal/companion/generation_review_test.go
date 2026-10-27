package companion

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRunningRequestReviewIsNotUnsentReplyReplacement(t *testing.T) {
	model := &fakeModel{answer: `{"action":"replace","messages":["new reply"]}`}
	store, e := NewStore(t.TempDir())
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
