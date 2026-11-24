package bot

import (
	"context"
	"testing"
	"wechat-bot/internal/companion"
)

func TestOrdinaryNewMessageLeavesOldPlanAlone(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"cancel"}`}
	w, c, n, in := planWorker(t, model)
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"原回复"}})
	readyPlans(w)
	m := fixtureMessage("new-request", in.Thread, "把新图片改成蓝色", "incoming")
	m.SenderID = in.Sender
	w.noticeConversationMessage(m, true)
	p := w.runtime.Plans[0]
	if p.State != "queued" || len(p.Updates) != 0 {
		t.Fatal("ordinary request revisited old plan", p.State, p.Updates)
	}
	w.reconcileUtterancePlans(t.Context())
	if model.calls != 0 || len(n.submissions) != 1 || p.State != "accepted" {
		t.Fatal("ordinary request paused the old send", model.calls, n.submissions, p.State)
	}
}

func TestExplicitCorrectionCancelsPreparedTextOnce(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"cancel","evidence_message_id":"correction","reason":"原请求者撤销"}`}
	w, c, n, in := planWorker(t, model)
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"旧内容"}})
	readyPlans(w)
	w.runtime.Deliveries = append(w.runtime.Deliveries, &delivery{Trace: in.TraceID, Thread: in.Thread, Input: in, Bubble: 0, Text: "旧内容", State: "prepared", Key: "prepared-key"})
	m := fixtureMessage("correction", in.Thread, "刚才那条我说错了，别发", "incoming")
	m.SenderID = in.Sender
	w.noticeConversationMessage(m, true)
	w.reconcileUtterancePlans(t.Context())
	p := w.runtime.Plans[0]
	if model.calls != 1 || p.State != "stopped" || p.StopReason != "explicit_correction_cancelled" || len(n.submissions) != 0 {
		t.Fatal("prepared text was not cancelled", model.calls, p.State, p.StopReason, n.submissions)
	}
	w.noticeConversationMessage(m, true)
	if len(p.Updates) != 0 || model.calls != 1 {
		t.Fatal("same correction was rechecked", p.Updates, model.calls)
	}
}

func TestCompletedPlanCanJudgeQuotedCorrectionAfterRestart(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"cancel","evidence_message_id":"correction","recall_sent_indices":[0],"reason":"已发内容有误"}`}
	w, c, _, in := planWorker(t, model)
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"旧内容"}})
	readyPlans(w)
	w.reconcileUtterancePlans(t.Context())
	p := w.runtime.Plans[0]
	if p.State != "accepted" || p.Index != 1 {
		t.Fatal("fixture did not finish", p.State, p.Index)
	}
	m := fixtureMessage("correction", in.Thread, "这个错了，请撤回", "incoming")
	m.SenderID = in.Sender
	quoted := fixtureMessage(in.TraceID, in.Thread, in.Text, "incoming")
	quoted.SenderID = in.Sender
	m.ResolvedQuote = &quoted
	w.noticeConversationMessage(m, true)
	if p.State != "review_pending" || p.ReviewReturnState != "accepted" {
		t.Fatal("completed plan ignored explicit correction", p.State, p.ReviewReturnState)
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal("completed correction did not survive restart", err)
	}
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	p = w.runtime.Plans[0]
	if model.calls != 1 || p.State != "stopped" || len(p.PendingRecall) != 1 || p.PendingRecall[0] != 0 {
		t.Fatal("accepted text was not selected for recall", model.calls, p.State, p.PendingRecall)
	}
}

func TestInvalidCorrectionJudgmentDoesNotStrandText(t *testing.T) {
	model := &planReviewModel{answer: `not-json`}
	w, c, n, in := planWorker(t, model)
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"原回复"}})
	readyPlans(w)
	m := fixtureMessage("correction", in.Thread, "刚才那条可能说错了", "incoming")
	m.SenderID = in.Sender
	w.noticeConversationMessage(m, true)
	w.reconcileUtterancePlans(t.Context())
	p := w.runtime.Plans[0]
	if model.calls != 1 || p.State != "queued" || len(p.Updates) != 0 {
		t.Fatal("invalid one-time check blocked delivery", model.calls, p.State, p.Updates)
	}
	w.noticeConversationMessage(m, true)
	w.reconcileUtterancePlans(t.Context())
	if model.calls != 1 || len(n.submissions) != 1 || p.State != "accepted" {
		t.Fatal("correction triggered a loop", model.calls, n.submissions, p.State)
	}
}
