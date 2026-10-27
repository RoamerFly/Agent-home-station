package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAutonomyOutcomesSeparatePlansFromAcceptedRuns(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	c := enabledConfig()
	c.Permissions.Remember = true
	c.Permissions.Proactive = true
	c.ProactiveThreads = []string{"friend"}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	in := Input{Channel: "wechat", Account: "a", Thread: "friend", Text: "最近喜欢的音乐", TraceID: "autonomous-1", Mode: "proactive"}
	_ = s.Observe(in, c, now.Add(-time.Hour))
	for i := 0; i < 2; i++ {
		if err := s.ProactiveOutcome(in, "reply", "prepared", c, now); err != nil {
			t.Fatal(err)
		}
	}
	st := s.ProactiveState("wechat", "a", "friend", c, now)
	if st.Outcomes.ReplyPlans != 1 || st.Outcomes.Accepted != 0 {
		t.Fatal(st)
	}
	for _, key := range []string{"receipt-1", "receipt-2"} {
		if err := s.AcceptedDelivery(in, "真实接受的消息", key, c, now); err != nil {
			t.Fatal(err)
		}
	}
	st = s.ProactiveState("wechat", "a", "friend", c, now)
	if st.Outcomes.Accepted != 1 {
		t.Fatal("bubbles counted as separate runs", st)
	}
	in.TraceID = "autonomous-2"
	_ = s.ProactiveOutcome(in, "failed", "provider_unavailable", c, now)
	if st = s.ProactiveState("wechat", "a", "friend", c, now); st.Outcomes.Failed != 1 {
		t.Fatal(st)
	}
	if st = s.ProactiveState("wechat", "a", "friend", c, now.Add(24*time.Hour)); st.Outcomes.Accepted != 0 {
		t.Fatal("day rollover", st)
	}
}

type autonomousMediaFixture struct{ *toolFixture }

func (*autonomousMediaFixture) Media(id string) (GeneratedMedia, bool) {
	return GeneratedMedia{ID: id, Kind: "image", Title: "合成图片"}, strings.HasPrefix(id, "image-")
}

func TestMoreThanThreeVerifiedMediaAreNotRejected(t *testing.T) {
	model := &fakeModel{answer: `{"action":"reply","messages":[],"media_ids":["image-1","image-2","image-3","image-4"]}`}
	e, err := NewPreview(enabledConfig(), model, "")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = &autonomousMediaFixture{&toolFixture{}}
	d, err := e.Respond(context.Background(), Input{Mode: "direct", Preview: true, Text: "把准备好的内容给我"})
	if err != nil || d.Action != "reply" || len(d.MediaIDs) != 4 {
		t.Fatal(d, err)
	}
}

func TestTaskTimeoutDefaultsToWorkerCancellation(t *testing.T) {
	c := enabledConfig()
	parent, stop := context.WithCancel(context.Background())
	ctx, cancel := c.TaskContext(parent)
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("unexpected task-wide deadline")
	}
	stop()
	if ctx.Err() != context.Canceled {
		t.Fatal(ctx.Err())
	}
	c.Conversation = ConversationDefaults()
	c.Conversation.TaskTimeoutSeconds = 7200
	ctx, cancel = c.TaskContext(context.Background())
	defer cancel()
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) < 119*time.Minute {
		t.Fatal("explicit timeout not honored")
	}
}

func TestOutputCountsAreRecommendationsNotDecisionRejection(t *testing.T) {
	c := enabledConfig()
	c.MaxBubbles = 1
	raw, _ := json.Marshal(Decision{Action: "reply", Messages: []string{"第一点", "第二点", "第三点", "第四点"}})
	model := &fakeModel{answer: string(raw)}
	e, err := NewPreview(c, model, "")
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.Respond(context.Background(), Input{Mode: "direct", Preview: true, Text: "分四点发给我"})
	if err != nil || d.Action != "reply" || len(d.Messages) != 4 {
		t.Fatal(d, err)
	}
}
