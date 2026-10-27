package bot

import (
	"context"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type planReviewModel struct {
	answer string
	calls  int
	turns  []map[string]string
}

func (m *planReviewModel) Chat(_ context.Context, _ string, turns []map[string]string) (string, error) {
	m.calls++
	m.turns = turns
	return m.answer, nil
}
func planWorker(t *testing.T, model *planReviewModel) (*Worker, *companion.Engine, *acceptingProgressNative, companion.Input) {
	t.Helper()
	c := companion.Defaults()
	c.Enabled = true
	c.Conversation = companion.ConversationDefaults()
	c.Permissions.Remember = true
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	c.ReplyMinMS = 0
	c.ReplyMaxMS = 0
	store, _ := companion.NewStore(t.TempDir())
	engine, err := companion.New(c, store, model, "test")
	if err != nil {
		t.Fatal(err)
	}
	n := &acceptingProgressNative{reliableFake: reliableFake{intents: map[string]linuxnative.ToolStatus{}}}
	o := DefaultOptions()
	o.RandomDelay = false
	w, err := New(Config{StateDir: t.TempDir(), Options: &o, AllowedThreads: []string{"filehelper", "room@chatroom", "friend"}}, &fakeSource{}, engine, n)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "original", Sender: "one", Text: "聊一聊", Mode: "direct", Directed: true, QuoteServerID: "77"}
	if err = store.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	return w, engine, n, in
}
func readyPlans(w *Worker) {
	w.runtime.SendGates = nil // Advance the simulated pacing clock.
	for _, p := range w.runtime.Plans {
		p.InboxChecked = true
		p.Next = time.Time{}
	}
}
func TestSemanticPlanWaitsForFreshInboxThenCanReplaceOnlyUnsentTail(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"replace","messages":["改成简短的补充"]}`}
	w, c, n, in := planWorker(t, model)
	w.queueUtterancePlan(c, in, companion.Decision{Action: "reply", Messages: []string{"这是第一条完整意思", "旧的第二条", "旧的第三条"}})
	w.reconcileUtterancePlans(context.Background())
	if len(n.submissions) != 0 {
		t.Fatal("sent before new inbox scan")
	}
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if len(n.submissions) != 1 {
		t.Fatal(n.submissions)
	}
	m := fixtureMessage("new", "filehelper", "不用说这么多，简单点", "incoming")
	m.SenderID = "one"
	w.noticeConversationMessage(m, true)
	if w.runtime.Plans[0].State != "review_pending" {
		t.Fatal("tail not paused")
	}
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if model.calls != 1 || len(n.submissions) != 1 || w.runtime.Plans[0].Messages[0] != "这是第一条完整意思" {
		t.Fatal("review rewrote accepted message or sent too early")
	}
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if len(n.submissions) != 2 || !strings.Contains(n.submissions[1].Text, "简短的补充") || w.runtime.Plans[0].State != "accepted" {
		t.Fatal("stale tail still sent", n.submissions)
	}
}
func TestPlanRestartKeepsExactTraceQuoteAndDeduplicatesAcceptedMemory(t *testing.T) {
	w, c, n, in := planWorker(t, &planReviewModel{})
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"已接受的第一条", "下一条"}})
	readyPlans(w)
	// Simulate accepted native receipt and memory commit before plan advancement.
	_, err := w.sendText(context.Background(), in, 0, "已接受的第一条")
	if err != nil {
		t.Fatal(err)
	}
	p := w.runtime.Plans[0]
	if err = c.Store.AcceptedDelivery(in, "已接受的第一条", p.ID+"-0", c.Config, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	if w.runtime.Plans[0].Input.TraceID != in.TraceID || w.runtime.Plans[0].Quote == nil || w.runtime.Plans[0].Quote.ServerID != "77" {
		t.Fatal("identity/reference lost on restart")
	}
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if len(n.submissions) != 1 || w.runtime.Plans[0].Index != 1 {
		t.Fatal("accepted first bubble replayed")
	}
	scope := c.Store.Context(in, c.Config, time.Now())
	count := 0
	for _, turn := range scope.Messages {
		if turn.Text == "已接受的第一条" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("accepted memory duplicated", count)
	}
}
func TestSleepingPlanDoesNotBlockAnotherConversationAndCancelDoesNotCancelMedia(t *testing.T) {
	w, c, n, in := planWorker(t, &planReviewModel{answer: `{"action":"cancel"}`})
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"旧内容"}})
	w.runtime.Plans[0].Next = time.Now().Add(time.Hour)
	w.runtime.Plans[0].InboxChecked = true
	other := in
	other.Thread = "friend"
	other.TraceID = "friend-request"
	_ = c.Store.Observe(other, c.Config, time.Now())
	w.queueUtterancePlan(c, other, companion.Decision{Messages: []string{"另一个会话的回复"}})
	w.runtime.Plans[1].InboxChecked = true
	w.reconcileUtterancePlans(context.Background())
	if len(n.submissions) != 1 || n.submissions[0].ThreadID != "friend" {
		t.Fatal("sleep blocked another thread", n.submissions)
	}
	w.runtime.Media = []*mediaDelivery{{Kind: "file", State: "running", Input: in, Trace: in.TraceID}}
	w.noticeConversationMessage(fixtureMessage("stop", "filehelper", "先别说了", "incoming"), true)
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if w.runtime.Plans[0].State != "stopped" || w.runtime.Media[0].State != "running" {
		t.Fatal("text cancellation cancelled independent file")
	}
}
func TestShortMessageAggregationPreservesEachIdentityAndDoesNotMixSpeakers(t *testing.T) {
	w, c, _, in := planWorker(t, &planReviewModel{})
	current := fixtureMessage("original", "room@chatroom", "帮我看看", "incoming")
	current.SenderID = "one"
	stamp := time.Now().Add(-time.Second)
	current.CreatedAt = stamp.Format(time.RFC3339Nano)
	peer := fixtureMessage("peer", "room@chatroom", "再解释这个词", "incoming")
	peer.SenderID = "one"
	peer.CreatedAt = stamp.Add(500 * time.Millisecond).Format(time.RFC3339Nano)
	other := peer
	other.ID = "other"
	other.SenderID = "two"
	w.source = &fakeSource{messages: map[string]linuxnative.Message{"peer": peer, "other": other}}
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", "original", current.ThreadID, "incoming", "remote"), Phase: "processing"}, {Event: fixtureEvent("2", "peer", current.ThreadID, "incoming", "remote"), Phase: "received"}, {Event: fixtureEvent("3", "other", current.ThreadID, "incoming", "remote"), Phase: "received"}}
	in.Thread = current.ThreadID
	in.Text = current.Body
	w.aggregateConversation(context.Background(), c, current, &in)
	if !strings.Contains(in.Text, "再解释这个词") || w.runtime.Inbox[1].Phase != "aggregated" || w.runtime.Inbox[2].Phase != "received" {
		t.Fatal("aggregation dropped or mixed messages", in.Text, w.runtime.Inbox)
	}
	scope := c.Store.Context(in, c.Config, time.Now())
	seen := map[string]bool{}
	for _, turn := range scope.Messages {
		seen[turn.TraceID] = true
	}
	if !seen["original"] || !seen["peer"] || seen["other"] {
		t.Fatal("individual memory identities lost", seen)
	}
}
func TestWakeIsIndependentBoundedAndOnlyForOwnerSelectedTargets(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"silent","messages":[]}`}
	w, c, _, in := planWorker(t, model)
	c.Config.Conversation.AllowWake = true
	c.Config.Permissions.Proactive = true
	c.Config.ProactiveThreads = []string{"filehelper"}
	w.scheduleConversationWake(c, in, 30, true)
	if len(w.runtime.Wakes) != 1 {
		t.Fatal("no wake")
	}
	w.runtime.Wakes[0].Due = time.Now().Add(-time.Second)
	if !w.tickConversationWake(context.Background()) || model.calls != 1 || w.runtime.Wakes[0].State != "completed" {
		t.Fatal("wake required a new question")
	}
	if w.tickConversationWake(context.Background()) || model.calls != 1 {
		t.Fatal("wake replayed")
	}
	in.TraceID = "another"
	w.scheduleConversationWake(c, in, 30, true)
	reply := fixtureMessage("human-replied", "filehelper", "好呀", "incoming")
	reply.SenderID = in.Sender
	w.noticeConversationMessage(reply, true)
	if w.runtime.Wakes[1].State != "stopped" {
		t.Fatal("wait-for-reply not cancelled")
	}
	in.Thread = "friend"
	in.TraceID = "not-selected"
	w.scheduleConversationWake(c, in, 30, false)
	if len(w.runtime.Wakes) != 2 {
		t.Fatal("unselected wake target admitted")
	}
}

func TestPlanPollsExistingReceiptWithoutWaitingForResendTimerOrSubmittingAgain(t *testing.T) {
	w, c, n, in := planWorker(t, &planReviewModel{})
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"只核对一次"}})
	readyPlans(w)
	d := &delivery{Trace: in.TraceID, Thread: in.Thread, Input: in, Bubble: 0, Text: "只核对一次", State: "waiting", ID: "existing-job", Key: "existing-job", Created: time.Now(), Next: time.Now().Add(time.Hour)}
	w.runtime.Deliveries = append(w.runtime.Deliveries, d)
	n.intents[d.ID] = linuxnative.ToolStatus{ID: d.ID, State: "accepted", ThreadID: in.Thread, ServerID: "123"}
	w.reconcileUtterancePlans(context.Background())
	if len(n.submissions) != 0 || w.runtime.Plans[0].State != "accepted" || d.State != "accepted" {
		t.Fatal("receipt check waited for resend timer or resubmitted", len(n.submissions), w.runtime.Plans[0], d)
	}
}
func TestNewContextPreventsSubmissionOfPreparedOldText(t *testing.T) {
	w, c, n, in := planWorker(t, &planReviewModel{answer: `{"action":"cancel"}`})
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"不该继续的旧内容"}})
	readyPlans(w)
	w.runtime.Deliveries = append(w.runtime.Deliveries, &delivery{Trace: in.TraceID, Thread: in.Thread, Input: in, Bubble: 0, Text: "不该继续的旧内容", State: "prepared", Key: "never-submitted", Created: time.Now()})
	w.noticeConversationMessage(fixtureMessage("new-context", "filehelper", "不用说了", "incoming"), true)
	w.reconcileDeliveries(context.Background())
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if len(n.submissions) != 0 || w.runtime.Plans[0].State != "stopped" || w.runtime.Deliveries[0].State != "stopped" {
		t.Fatal("unsubmitted stale text escaped review")
	}
}
