package bot

import (
	"context"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/linuxnative"
)

type questionJudgementModel struct{ calls int }

func (m *questionJudgementModel) Chat(context.Context, string, []map[string]string) (string, error) {
	m.calls++
	return `{"action":"silent","messages":[]}`, nil
}

func TestModelFailureRecordsBoundedDiagnosticsWithoutSendingFallback(t *testing.T) {
	message := fixtureMessage("failure", "filehelper", "/ai 还在加功能[呲牙]", "outgoing")
	source := &fakeSource{messages: map[string]linuxnative.Message{message.ID: message}}
	sender := &fakeSender{}
	model := &fakeModel{err: &deepseek.RequestError{Diagnostics: deepseek.Diagnostics{Code: "model_service_unavailable", Stage: "http_status", HTTPStatus: 503, Attempts: 2, MaxAttempts: 2, DurationMS: 1200}}}
	w := newTestWorker(t, source, model, sender)
	var records []Record
	w.config.Record = func(r Record) { records = append(records, r) }
	w.handle(context.Background(), fixtureEvent("1", message.ID, message.ThreadID, message.Direction, "wechat"))
	for _, r := range records {
		if r.Event == "model_failed" {
			if r.ModelHTTPStatus != 503 || r.ModelAttempts != 2 || r.ModelMaxAttempts != 2 || r.ModelStage != "http_status" || r.Reason != "model_service_unavailable" || sender.calls != 0 {
				t.Fatal("diagnostic lost or fallback sent", r, sender.calls)
			}
			return
		}
	}
	t.Fatal("model failure not recorded")
}

func TestHistoricalToolQuestionPassesBothCooldownsForJudgement(t *testing.T) {
	c := companion.Defaults()
	c.Enabled = true
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	c.Permissions.ObserveGroups = true
	c.Permissions.AutonomousGroups = true
	store, _ := companion.NewStore(t.TempDir())
	model := &questionJudgementModel{}
	engine, err := companion.New(c, store, model, "test")
	if err != nil {
		t.Fatal(err)
	}
	seed := companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Text: "seed", Mode: "social"}
	if err := store.Observe(seed, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimDecision(seed, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	message := fixtureMessage("question", "room@chatroom", "你目前可调用的mcp工具有哪些", "incoming")
	source := &fakeSource{messages: map[string]linuxnative.Message{"question": message}}
	sender := &fakeSender{}
	var activity []Record
	w, err := New(Config{StateDir: t.TempDir(), AllowedThreads: []string{"room@chatroom"}, Cooldown: time.Minute, GroupRules: map[string]GroupRule{"room@chatroom": {Mode: "social"}}, Record: func(r Record) { activity = append(activity, r) }}, source, engine, sender)
	if err != nil {
		t.Fatal(err)
	}
	w.last[message.ThreadID] = time.Now()
	w.handle(context.Background(), fixtureEvent("1", message.ID, message.ThreadID, "incoming", "wechat"))
	if model.calls != 1 || sender.calls != 0 {
		t.Fatal("question was dropped or forced to reply", model.calls, sender.calls)
	}
	seenReview, seenDecision := false, false
	for _, r := range activity {
		seenReview = seenReview || r.Reason == "direct_question_review"
		seenDecision = seenDecision || r.Event == "character_decision" && r.Reason == "model_chose_silence"
	}
	if !seenReview || !seenDecision {
		t.Fatal("missing judgement trace", activity)
	}
}
