package companion

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGroupInterjectionDoesNotReassignDeliveredAttachment(t *testing.T) {
	scope := Scope{Messages: []Turn{
		{Role: "user", Sender: "first"},
		{Role: "user", Sender: "second"},
		{Role: "assistant", Media: &MediaMemory{Kind: "file", RequesterID: "first", State: "accepted"}},
	}}
	for _, sender := range []string{"first", "second"} {
		history := groundedHistory(scope, Input{IsGroup: true, Sender: sender})
		if visible := history[2].Media != nil; visible != (sender == "first") {
			t.Fatalf("file delivery attributed to wrong member: sender=%s visible=%v", sender, visible)
		}
	}
}

func TestUnreadPacketRetainsOutgoingBubbleFromSameRequest(t *testing.T) {
	cfg := enabledConfig()
	cfg.Permissions.Remember = true
	cfg.Grounding = &GroundingSettings{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	model := &fakeModel{answer: `{"action":"silent","messages":[]}`}
	engine, err := New(cfg, store, model, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	original := syntheticNativeInput(Input{Channel: "wechat", Account: "test", Thread: "friend", Sender: "friend", Mode: "direct", Text: "请把昨天的示意图再发一次"}, "request-one")
	if err = store.Observe(original, cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = store.ObserveMedia(original, cfg, MediaMemory{Key: "receipt-one", State: "accepted", Kind: "image", Title: "fixture diagram"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	follow := syntheticNativeInput(Input{Channel: "wechat", Account: "test", Thread: "friend", Sender: "friend", Mode: "direct", Text: "我收到了"}, "follow-up")
	follow.UnreadMessages = []ConversationEvent{{MessageID: original.TraceID, Sender: original.Sender, Text: original.Text, At: original.MessageTime}}
	if _, err = engine.Respond(t.Context(), follow); err != nil {
		t.Fatal(err)
	}
	for _, turn := range model.turns {
		var packet struct{ Kind string }
		if json.Unmarshal([]byte(turn["content"]), &packet) == nil && (packet.Kind == "history_reply" || packet.Kind == "conversation_context") &&
			strings.Contains(turn["content"], "receipt-one") {
			return
		}
	}
	t.Fatal("unread aggregation removed the accepted image along with the inbound request")
}
