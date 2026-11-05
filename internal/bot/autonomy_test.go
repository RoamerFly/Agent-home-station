package bot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestAutonomousWakeSurvivesQuietRestartAndRechainsBeyondDefaults(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"silent","messages":[],"wake_after_seconds":9000,"wake_reason":"晚些看看大家讨论的新歌"}`}
	w, c, _, in := planWorker(t, model)
	c.Config.Conversation.AllowWake = true
	c.Config.Permissions.Proactive = true
	c.Config.ProactiveThreads = []string{in.Thread}
	w.scheduleConversationWake(c, in, 7200, false, "关注最近提到的新歌")
	if len(w.runtime.Wakes) != 1 || time.Until(w.runtime.Wakes[0].Due) < 119*time.Minute {
		t.Fatal("suggested range clamped explicit plan")
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	w.runtime.Wakes[0].Due = time.Now().Add(-24 * time.Hour)
	c.Config.QuietStart = "00:00"
	c.Config.QuietEnd = "23:59"
	if w.tickConversationWake(context.Background()) || w.runtime.Wakes[0].State != "waiting" {
		t.Fatal("quiet period discarded plan")
	}
	c.Config.QuietEnd = "00:00"
	// Existing evaluation counts must not block a meaningful later review.
	for i := 0; i < 7; i++ {
		if err := c.Store.ClaimConversationWake(in, c.Config, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if !w.tickConversationWake(context.Background()) || model.calls != 1 || len(w.runtime.Wakes) != 1 || w.runtime.Wakes[0].State != "completed" {
		t.Fatal("wake did not continue", model.calls, w.runtime.Wakes)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(model.turns[len(model.turns)-1]["content"]), &payload); err != nil {
		t.Fatal(err)
	}
	runtimeContext, _ := payload["runtime_context"].(map[string]any)
	runtimeText, _ := runtimeContext["text"].(string)
	if requestActorFromPacket(payload) != "" || payload["text"] != "" || !strings.Contains(runtimeText, "关注最近提到的新歌") || runtimeContext["can_support_user_facts"] != false {
		t.Fatal("plan lost or old owner identity inherited", payload)
	}
}

func TestWaitForReplyIgnoresOtherGroupMembers(t *testing.T) {
	w, c, _, in := planWorker(t, &planReviewModel{})
	in.Thread = "room@chatroom"
	c.Config.Conversation.AllowWake = true
	c.Config.Permissions.Proactive = true
	c.Config.ProactiveThreads = []string{in.Thread}
	w.scheduleConversationWake(c, in, 1, true)
	other := fixtureMessage("other", "room@chatroom", "群友在互聊", "incoming")
	other.SenderID = "someone-else"
	w.noticeConversationMessage(other, true)
	if w.runtime.Wakes[0].State != "waiting" {
		t.Fatal("unrelated group message cancelled plan")
	}
	other.ID = "requester-replied"
	other.SenderID = in.Sender
	w.noticeConversationMessage(other, true)
	if w.runtime.Wakes[0].State != "stopped" {
		t.Fatal("original participant reply not observed")
	}
}

func TestWakeWaitsForOriginalReceiptAndNeverRunsCancelledTask(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"silent","messages":[]}`}
	w, c, _, in := planWorker(t, model)
	c.Config.Conversation.AllowWake = true
	c.Config.Permissions.Proactive = true
	c.Config.ProactiveThreads = []string{in.Thread}
	w.scheduleConversationWake(c, in, 1, false, "等原回复回执")
	if len(w.runtime.Wakes) != 1 {
		t.Fatal("wake not scheduled")
	}
	w.runtime.Wakes[0].Due = time.Now().Add(-time.Second)
	w.runtime.Deliveries = append(w.runtime.Deliveries, &delivery{Trace: in.TraceID, Thread: in.Thread, Input: in, State: "waiting", Key: "original-key"})
	if w.wakeAwaitingReceipt("other-thread", in.TraceID) {
		t.Fatal("same trace in another thread incorrectly blocks wake")
	}
	if w.tickConversationWake(context.Background()) || w.runtime.Wakes[0].State != "waiting" || model.calls != 0 {
		t.Fatal("wake compensated for an unresolved original receipt", w.runtime.Wakes[0], model.calls)
	}

	// Once a durable task has been cancelled, neither an old persisted wake nor
	// a new model decision may revive it.  It is deliberately distinct from an
	// unrelated new group message, which remains ordinary context.
	in.TaskID = "cancelled-task"
	w.config.AgentTaskActive = func(companion.Input) bool { return false }
	w.scheduleConversationWake(c, in, 1, false, "不应重新开始")
	if len(w.runtime.Wakes) != 1 {
		t.Fatal("cancelled task scheduled a new wake")
	}
	w.runtime.Wakes[0].Input = in
	w.runtime.Wakes[0].Due = time.Now().Add(-time.Second)
	w.runtime.Deliveries = nil
	if w.tickConversationWake(context.Background()) || w.runtime.Wakes[0].State != "stopped" || model.calls != 0 {
		t.Fatal("cancelled task wake was revived", w.runtime.Wakes[0], model.calls)
	}
}

func TestLargerReplyPlanSurvivesRestartAndKeepsSpacing(t *testing.T) {
	w, c, _, in := planWorker(t, &planReviewModel{})
	messages := make([]string, 20)
	for i := range messages {
		messages[i] = "一条完整的测试内容"
	}
	if !w.queueUtterancePlan(c, in, companion.Decision{Messages: messages}) {
		t.Fatal("plan not queued")
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal("larger plan cannot recover", err)
	}
	if len(w.runtime.Plans[0].Messages) != 20 {
		t.Fatal("tail lost")
	}
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if p := w.runtime.Plans[0]; p.Index != 1 || p.State == "accepted" {
		t.Fatal("multiple messages bypassed paced queue", p)
	}
}

func TestFutureReviewDoesNotDisableNormalAutonomousEvaluation(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"silent","messages":[]}`}
	w, c, _, in := planWorker(t, model)
	c.Config.Conversation.AllowWake = true
	c.Config.Permissions.Proactive = true
	c.Config.ProactiveThreads = []string{in.Thread}
	if err := c.Store.ClaimConversationWake(in, c.Config, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	w.scheduleConversationWake(c, in, 7200, false, "两小时后跟进")
	w.tickCharacter(context.Background())
	if model.calls != 1 || len(w.runtime.Wakes) != 1 || w.runtime.Wakes[0].State != "waiting" {
		t.Fatal("future plan blocked normal activity", model.calls)
	}
	if !strings.Contains(model.turns[len(model.turns)-1]["content"], "两小时后跟进") {
		t.Fatal("model could not see existing plan")
	}
	in.TraceID = "reschedule"
	w.scheduleConversationWake(c, in, 300, false, "新上下文适合五分钟后")
	if len(w.runtime.Wakes) != 2 || w.runtime.Wakes[0].State != "superseded" || w.runtime.Wakes[1].Purpose != "新上下文适合五分钟后" {
		t.Fatal("plan replacement not persisted")
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	if w.runtime.Wakes[1].State != "waiting" {
		t.Fatal("new plan lost")
	}
}
