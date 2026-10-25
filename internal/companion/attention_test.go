package companion

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestSecondPersonQuestionsRequestJudgementWithoutInventingMentions(t *testing.T) {
	for _, text := range []string{"你目前可调用的mcp工具有哪些", "你觉得这个怎么样？", "请问你能不能发语音", "那你现在还在吗"} {
		if !NeedsQuestionReview(text) {
			t.Fatal("question missed", text)
		}
	}
	for _, text := range []string{"他想问你有哪些工具", "你刚刚说得对", "天气真不错", "你们觉得怎么样", "“你有哪些工具”是原话"} {
		if NeedsQuestionReview(text) {
			t.Fatal("ambient or quoted text promoted", text)
		}
	}
	c := enabledConfig()
	c.Permissions.ObserveGroups = true
	c.Permissions.AutonomousGroups = true
	store, _ := NewStore(t.TempDir())
	model := &fakeModel{answer: `{"action":"silent","messages":[]}`}
	e, err := New(c, store, model, "test")
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "friend", Mode: "social", IsGroup: true, Text: "天气不错"}
	if _, err = e.Respond(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	input.Text = "你目前可调用的mcp工具有哪些"
	d, err := e.Respond(context.Background(), input)
	if err != nil || model.calls != 2 || d.Action != "silent" || d.Reason != "model_chose_silence" {
		t.Fatal("question skipped or reply forced", d, err, model.calls)
	}
	var current map[string]any
	_ = json.Unmarshal([]byte(model.turns[len(model.turns)-1]["content"]), &current)
	if current["directed"] != false || current["review_required"] != true || current["mode"] != "social" {
		t.Fatal("question was misrepresented as @", current)
	}
	input.Text = "天气还是不错"
	d, err = e.Respond(context.Background(), input)
	if err != nil || model.calls != 2 || d.Reason != "decision_cooldown" {
		t.Fatal("ambient cooldown removed", d, err)
	}
}

func TestQuestionReviewRetainsBudgetsPermissionsAndQuietHours(t *testing.T) {
	c := enabledConfig()
	c.Permissions.ObserveGroups = true
	c.Permissions.AutonomousGroups = true
	c.MaxRepliesPerHour = 1
	store, _ := NewStore(t.TempDir())
	model := &fakeModel{answer: `{"action":"silent","messages":[]}`}
	e, _ := New(c, store, model, "test")
	input := Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Mode: "social", Text: "你目前有哪些工具", ReviewRequired: true}
	for i := 0; i < 2; i++ {
		if _, err := e.Respond(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	d, err := e.Respond(context.Background(), input)
	if err != nil || d.Reason != "hourly_decision_budget" || model.calls != 2 {
		t.Fatal("question bypassed hourly budget", d, err, model.calls)
	}
	e.Config.Permissions.ObserveGroups = false
	d, _ = e.Respond(context.Background(), input)
	if d.Reason != "group_observation_or_social_permission_denied" || model.calls != 2 {
		t.Fatal("question bypassed observation permission", d)
	}
	e.Config.Permissions.ObserveGroups = true
	now := time.Now().In(location(c.Timezone))
	e.Config.QuietStart = now.Add(-time.Hour).Format("15:04")
	e.Config.QuietEnd = now.Add(time.Hour).Format("15:04")
	d, _ = e.Respond(context.Background(), input)
	if d.Reason != "quiet_hours" || model.calls != 2 {
		t.Fatal("question bypassed quiet hours", d)
	}
}
