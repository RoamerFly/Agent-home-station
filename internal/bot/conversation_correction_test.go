package bot

import (
	"context"
	"testing"
	"time"
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

func TestCorrectedReferenceStopsAlreadyPreparedVideoDelivery(t *testing.T) {
	w, n, _, in := mediaWorker(t)
	in.Text = "生成大肥鱼形象视频"
	w.runtime.Media = []*mediaDelivery{{Key: "old-video", Trace: in.TraceID, Input: in, SourceID: "old-source", Kind: "video", State: "prepared", Created: time.Now()}}
	ordinary := fixtureMessage("ordinary", in.Thread, "我又发了一张图", "incoming")
	ordinary.SenderID = in.Sender
	w.cancelCreativeMediaForCorrection(ordinary)
	if w.runtime.Media[0].State != "prepared" {
		t.Fatal("ordinary message stopped old video")
	}
	correction := fixtureMessage("corrected-reference", in.Thread, "只用这张参考图", "incoming")
	correction.SenderID = "another-member"
	w.cancelCreativeMediaForCorrection(correction)
	if w.runtime.Media[0].State != "prepared" {
		t.Fatal("other group member stopped old video")
	}
	correction.SenderID = in.Sender
	w.cancelCreativeMediaForCorrection(correction)
	if d := w.runtime.Media[0]; d.State != "stopped" || !d.UserCancelled || d.CancelTrace != correction.ID || n.sends != 0 {
		t.Fatalf("old prepared video remained deliverable: %+v sends=%d", d, n.sends)
	}
}

func TestDirectNotCorrectionMatchesOriginalRequestOnly(t *testing.T) {
	in := companion.Input{Thread: "room@chatroom", Sender: "member", Text: "请用柯南声线唱歌"}
	corrected := fixtureMessage("correction", in.Thread, "是 konan 的声线，不是柯南", "incoming")
	corrected.SenderID = in.Sender
	if !explicitCorrectionToPriorInput(in, corrected) {
		t.Fatal("direct correction did not reach the active request")
	}
	other := fixtureMessage("new", in.Thread, "这不是我的歌", "incoming")
	other.SenderID = in.Sender
	if explicitCorrectionToPriorInput(in, other) {
		t.Fatal("unrelated negative sentence revisited the old request")
	}
}

func TestSameSenderUploadedReferenceCorrectionSelectsCreativeTurn(t *testing.T) {
	in := companion.Input{Thread: "103@chatroom", Sender: "owner", Text: "生成一段以大肥鱼自己形象为素材的视频"}
	corrected := fixtureMessage("reference-rule", in.Thread, "只用这张参考图", "incoming")
	corrected.SenderID = in.Sender
	if !explicitCorrectionToPriorInput(in, corrected) {
		t.Fatal("explicit new reference did not select active video request")
	}
	corrected.SenderID = "another-member"
	if explicitCorrectionToPriorInput(in, corrected) {
		t.Fatal("another member changed original video request")
	}
	corrected.SenderID = in.Sender
	in.Text = "帮我查明天气"
	if explicitCorrectionToPriorInput(in, corrected) {
		t.Fatal("reference phrase selected unrelated turn")
	}
	corrected.Body = "我又发了一张图"
	in.Text = "生成一段视频"
	if explicitCorrectionToPriorInput(in, corrected) {
		t.Fatal("ordinary image follow-up revisited video request")
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

func TestLegacyOrdinaryMessageReviewIsClearedOnLoad(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"cancel"}`}
	w, c, n, in := planWorker(t, model)
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"原回复"}})
	p := w.runtime.Plans[0]
	p.State, p.ReviewReason, p.ReviewReturnState = "review_pending", "new_message", "queued"
	p.Updates = []companion.ConversationEvent{{MessageID: "ordinary", Sender: in.Sender, Text: "再说一件新事"}}
	w.runtime.Deliveries = append(w.runtime.Deliveries, &delivery{Trace: in.TraceID, Thread: in.Thread, Input: in, Bubble: 0, Text: "原回复", State: "prepared", Key: "original-key"})
	if err := w.saveRuntime(); err != nil {
		t.Fatal(err)
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	p = w.runtime.Plans[0]
	if p.State != "queued" || p.ReviewReason != "" || p.ReviewReturnState != "" || len(p.Updates) != 0 || len(p.ReviewedUpdateIDs) != 1 || p.ReviewedUpdateIDs[0] != "ordinary" {
		t.Fatal("old ordinary message still requested a review", p)
	}
	if len(w.runtime.Deliveries) != 1 || w.runtime.Deliveries[0].Key != "original-key" || w.runtime.Deliveries[0].State != "prepared" {
		t.Fatal("old send intent changed", w.runtime.Deliveries)
	}
	readyPlans(w)
	w.reconcileUtterancePlans(t.Context())
	if model.calls != 0 || len(n.submissions) != 0 {
		t.Fatal("old ordinary message caused model review or duplicate send", model.calls, n.submissions)
	}
}

func TestLegacyCompletedReviewKeepsOneShotWake(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"cancel"}`}
	w, c, n, in := planWorker(t, model)
	c.Config.Conversation.AllowWake = true
	c.Config.Permissions.Proactive = true
	c.Config.ProactiveThreads = []string{in.Thread}
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"原回复"}})
	p := w.runtime.Plans[0]
	p.Index, p.Slot, p.Sent = 1, 1, []string{"原回复"}
	p.State, p.ReviewReason, p.ReviewReturnState = "review_pending", "new_message", "accepted"
	p.WakePending, p.WakeAfterSeconds = true, 90
	p.Updates = []companion.ConversationEvent{{MessageID: "ordinary", Sender: in.Sender, Text: "再说一件新事"}}
	w.runtime.Deliveries = append(w.runtime.Deliveries, &delivery{Trace: in.TraceID, Thread: in.Thread, Input: in, Bubble: 0, Text: "原回复", State: "accepted", Key: "original-key", ServerID: "server-message"})
	if err := w.saveRuntime(); err != nil {
		t.Fatal(err)
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	p = w.runtime.Plans[0]
	if p.State != "accepted" || !p.WakePending || p.Index != 1 || len(p.Sent) != 1 || w.runtime.Deliveries[0].ServerID != "server-message" {
		t.Fatal("accepted legacy reply lost its delivery or pending wake", p)
	}
	w.reconcileUtterancePlans(t.Context())
	w.reconcileUtterancePlans(t.Context())
	if p.WakePending || len(w.runtime.Wakes) != 1 || w.runtime.Wakes[0].Parent != in.TraceID || model.calls != 0 || len(n.submissions) != 0 {
		t.Fatal("legacy wake was lost, duplicated, or reviewed", p.WakePending, w.runtime.Wakes, model.calls, n.submissions)
	}
}
