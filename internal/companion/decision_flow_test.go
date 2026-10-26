package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestBurstOfNormalMessagesAllReachModel(t *testing.T) {
	c := enabledConfig()
	c.MaxRepliesPerHour = 1
	c.Permissions.ObserveGroups = true
	c.Permissions.AutonomousGroups = true
	store, _ := NewStore(t.TempDir())
	model := &fakeModel{answer: `{"action":"silent","messages":[]}`}
	e, err := New(c, store, model, "test")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		in := Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "friend", Mode: "social", IsGroup: true, Text: "群友正常聊天", TraceID: fmt.Sprint(i), HistoricalReview: i == 5}
		d, err := e.Respond(context.Background(), in)
		if err != nil || d.Reason != "model_chose_silence" {
			t.Fatal(i, d, err)
		}
	}
	if model.calls != 6 {
		t.Fatal("messages suppressed", model.calls)
	}
	var last map[string]any
	if json.Unmarshal([]byte(model.turns[len(model.turns)-1]["content"]), &last) != nil || last["historical_review"] != true || last["directed"] != false {
		t.Fatal("recovery lost or promoted to mention", last)
	}
}
