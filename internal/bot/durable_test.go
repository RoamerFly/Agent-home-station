package bot

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/sending"
)

type reliableFake struct {
	fakeSender
	intents     map[string]linuxnative.ToolStatus
	submissions []linuxnative.ToolSend
	rows        []map[string]any
	scanErr     error
	acceptRetry bool
}

func (n *reliableFake) Submit(_ context.Context, r linuxnative.ToolSend) (linuxnative.ToolStatus, error) {
	if s, ok := n.intents[r.IdempotencyKey]; ok {
		return s, nil
	}
	n.submissions = append(n.submissions, r)
	s := linuxnative.ToolStatus{ID: r.IdempotencyKey, ThreadID: r.ThreadID, State: "needs_review", Reason: "submission_outcome_unknown"}
	if n.acceptRetry && len(n.submissions) > 1 {
		s.State = "accepted"
		s.ServerID = "123"
	}
	n.intents[r.IdempotencyKey] = s
	return s, nil
}
func (n *reliableFake) LookupSend(_ context.Context, key string) (linuxnative.ToolStatus, error) {
	s, ok := n.intents[key]
	if !ok {
		return s, linuxnative.ErrToolNotFound
	}
	return s, nil
}
func (n *reliableFake) ToolSendStatus(ctx context.Context, id string) (linuxnative.ToolStatus, error) {
	return n.LookupSend(ctx, id)
}
func (n *reliableFake) Catalog(context.Context, string, string, string, string) (linuxnative.CatalogPage, error) {
	return linuxnative.CatalogPage{Items: n.rows}, n.scanErr
}
func durableWorker(t *testing.T, n *reliableFake) *Worker {
	w := newTestWorker(t, &fakeSource{}, &fakeModel{answer: "ok"}, &n.fakeSender)
	w.sender = n
	o := DefaultOptions()
	w.config.Options = &o
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestShareReplyQuotesOriginalWithoutFabricatingMention(t *testing.T) {
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w := durableWorker(t, n)
	w.config.Options.QuoteReplies = false
	in := companion.Input{Thread: "room@chatroom", TraceID: "shared-card", QuoteServerID: "77", QuoteSharedMessage: true, Directed: false, Mode: "social"}
	_, _ = w.sendText(context.Background(), in, 0, "这个分享标题挺有意思")
	if len(n.submissions) != 1 || n.submissions[0].Quote == nil || n.submissions[0].Quote.MessageID != "shared-card" || w.runtime.Deliveries[0].Input.Directed {
		t.Fatal("card reply lost its reference or fabricated an @", n.submissions)
	}
}
func TestUnknownResendRequiresTwoHealthyChecksAndContinuesAfterRestart(t *testing.T) {
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w := durableWorker(t, n)
	input := companion.Input{Thread: "filehelper", TraceID: "incoming", Directed: true, QuoteServerID: "77", Mode: "direct"}
	_, err := w.sendText(context.Background(), input, 0, "需要核对的内容")
	if !errors.Is(err, sending.ErrOutcomeUnknown) || len(n.submissions) != 1 || n.submissions[0].Quote == nil {
		t.Fatal("quote/unknown missing", err)
	}
	d := w.runtime.Deliveries[0]
	n.scanErr = errors.New("sync unavailable")
	d.Next = time.Time{}
	w.runtime.SendGates = nil
	w.reconcileDeliveries(context.Background())
	if d.Checks != 0 || len(n.submissions) != 1 {
		t.Fatal("unavailable sync interpreted as absence")
	}
	n.scanErr = nil
	d.Next = time.Time{}
	w.runtime.SendGates = nil
	w.reconcileDeliveries(context.Background())
	if d.Checks != 1 || len(n.submissions) != 1 {
		t.Fatal("retry before two checks")
	}
	// Persist and reload between checks: restart must not erase attempt budget.
	w2 := durableWorker(t, n)
	w2.config.StateDir = w.config.StateDir
	if err := w2.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	d = w2.runtime.Deliveries[0]
	d.Next = time.Time{}
	w.runtime.SendGates = nil
	w2.runtime.SendGates = nil
	w2.reconcileDeliveries(context.Background())
	if len(n.submissions) != 2 || d.Attempts != 1 {
		t.Fatal("one retry missing", len(n.submissions), d)
	}
	d.Next = time.Time{}
	w.runtime.SendGates = nil
	w2.runtime.SendGates = nil
	w2.reconcileDeliveries(context.Background())
	if len(n.submissions) != 2 || d.State == "stopped" || d.Checks != 1 {
		t.Fatal("unknown retry was stopped or sent without checks")
	}
	d.Next = time.Time{}
	w.runtime.SendGates = nil
	w2.runtime.SendGates = nil
	w2.reconcileDeliveries(context.Background())
	if len(n.submissions) != 3 || d.Attempts != 2 || len(d.PreviousKeys) != 2 || d.UncertainRounds != 3 {
		t.Fatal("persistent repeated retry missing", d)
	}
	first := n.submissions[0].IdempotencyKey
	late := n.intents[first]
	late.State = "accepted"
	late.ServerID = "late-receipt"
	n.intents[first] = late
	d.Next = time.Time{}
	w.runtime.SendGates = nil
	w2.runtime.SendGates = nil
	w2.reconcileDeliveries(context.Background())
	if d.State != "accepted" || len(n.submissions) != 3 {
		t.Fatal("late receipt on the original attempt was ignored")
	}
}
func TestSyncedOutgoingSuppressesUnknownRetry(t *testing.T) {
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w := durableWorker(t, n)
	_, _ = w.sendText(context.Background(), companion.Input{Thread: "filehelper", TraceID: "x"}, 0, "已经送达")
	n.rows = []map[string]any{{"direction": "outgoing", "body": "已经送达", "serverId": "999", "createdAt": time.Now().Format(time.RFC3339)}}
	d := w.runtime.Deliveries[0]
	d.Next = time.Time{}
	w.runtime.SendGates = nil
	w.reconcileDeliveries(context.Background())
	if d.State != "accepted" || len(n.submissions) != 1 {
		t.Fatal("synced send was resent")
	}
}

func TestPendingNativeSubmissionIsNeverResentWhileRunning(t *testing.T) {
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w := durableWorker(t, n)
	_, _ = w.sendText(context.Background(), companion.Input{Thread: "filehelper", TraceID: "pending"}, 0, "还在发送")
	d := w.runtime.Deliveries[0]
	s := n.intents[d.Key]
	s.State = "submitting"
	n.intents[d.Key] = s
	for i := 0; i < 3; i++ {
		d.Next = time.Time{}
		w.runtime.SendGates = nil
		w.reconcileDeliveries(context.Background())
	}
	if d.Checks != 0 || len(n.submissions) != 1 || d.Attempts != 0 {
		t.Fatal("running native job was resent")
	}
}

func TestDirectNameQuestionAndPacingBounds(t *testing.T) {
	w := newTestWorker(t, &fakeSource{}, &fakeModel{}, &fakeSender{})
	o := DefaultOptions()
	o.DelayMinSeconds = 2
	o.DelayMaxSeconds = 9
	w.config.Options = &o
	m := fixtureMessage("a", "room@chatroom", "大肥鱼，你觉得这个怎么样？", "incoming")
	if !w.addressed(m) {
		t.Fatal("direct name question missed")
	}
	m.Body = "别人提到了大肥鱼"
	if w.addressed(m) {
		t.Fatal("third-person mention treated as direct question")
	}
	for i := 0; i < 50; i++ {
		d := w.replyDelay(nil)
		if d < 2*time.Second || d > 9*time.Second {
			t.Fatal("pacing outside bounds", d)
		}
	}
}
func TestDurableInboxRecoversOldMessagesAndDoesNotReplayTerminalOnRestart(t *testing.T) {
	s := &fakeSource{messages: map[string]linuxnative.Message{}, events: []linuxnative.Event{}}
	for _, id := range []string{"a", "b"} {
		m := fixtureMessage(id, "filehelper", "/ai hello "+id, "outgoing")
		m.CreatedAt = time.Now().Add(-15 * time.Minute).Format(time.RFC3339)
		s.messages[id] = m
	}
	s.events = []linuxnative.Event{fixtureEvent("1", "a", "filehelper", "outgoing", "remote_sync"), fixtureEvent("2", "b", "filehelper", "outgoing", "remote_sync")}
	m := &fakeModel{answer: "answer"}
	sender := &fakeSender{}
	w := newTestWorker(t, s, m, sender)
	o := DefaultOptions()
	o.RandomDelay = false
	w.config.Options = &o
	if err := saveCheckpoint(filepath.Join(w.config.StateDir, "checkpoint.json"), checkpoint{Version: 1, ServerID: "server-1", Account: "test", Cursor: "0"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.before = func(string) {
		if len(w.runtime.Inbox) != 2 {
			t.Fatal("events not durably queued before model")
		}
		if m.calls == 2 {
			defer cancel()
		}
	}
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if m.calls != 2 || sender.calls != 2 {
		t.Fatal("backlog not judged one by one", m.calls, sender.calls)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	s.onPage = func(string) { cancel2() }
	w2, _ := New(w.config, s, m, sender)
	if err := w2.Run(ctx2); err != nil {
		t.Fatal(err)
	}
	if m.calls != 2 || sender.calls != 2 {
		t.Fatal("restart replayed terminal inbox")
	}
}
func TestInterruptedModelWorkRemainsRecoverable(t *testing.T) {
	s := &fakeSource{messages: map[string]linuxnative.Message{"a": fixtureMessage("a", "filehelper", "/ai hi", "outgoing")}}
	m := &fakeModel{err: context.Canceled}
	w := newTestWorker(t, s, m, &fakeSender{})
	o := DefaultOptions()
	w.config.Options = &o
	_ = w.loadRuntime("server-1")
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", "a", "filehelper", "outgoing", "remote_sync"), Phase: "processing"}}
	ctx, cancel := context.WithCancel(context.Background())
	m.before = func(string) { cancel() }
	_ = w.drainInbox(ctx)
	if w.runtime.Inbox[0].Phase != "processing" {
		t.Fatal("cancelled model request marked terminal")
	}
	m.err = nil
	m.before = nil
	m.answer = "hi"
	if err := w.drainInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.runtime.Inbox[0].Phase != "replied" {
		t.Fatal("interrupted request did not recover")
	}
}

func TestBackgroundCaptionQuotesOriginalMessage(t *testing.T) {
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w := durableWorker(t, n)
	_, _ = w.sendText(context.Background(), companion.Input{Thread: "filehelper", TraceID: "music-task-123", OriginTraceID: "native-incoming", QuoteServerID: "77", Directed: true}, 0, "生日歌补上")
	if n.submissions[0].Quote.MessageID != "native-incoming" {
		t.Fatal("synthetic task used as quote")
	}
}
func TestUncertainBackoffIsRandomAndGrowsUntilCeiling(t *testing.T) {
	distinct := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		a, b, c := deliveryBackoff(120, 1), deliveryBackoff(120, 2), deliveryBackoff(120, 999)
		if a < 120*time.Second || a > 240*time.Second || b < 240*time.Second || b > 480*time.Second || c < time.Hour || c > 2*time.Hour {
			t.Fatal(a, b, c)
		}
		distinct[a] = true
	}
	if len(distinct) < 2 {
		t.Fatal("backoff was fixed")
	}
}
func TestUncertainDeliveryDoesNotExpireAt24HoursAndHonorsDisable(t *testing.T) {
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w := durableWorker(t, n)
	_, _ = w.sendText(context.Background(), companion.Input{Thread: "filehelper", TraceID: "older"}, 0, "持续补发")
	d := w.runtime.Deliveries[0]
	d.Created = time.Now().Add(-48 * time.Hour)
	w.config.Options.RetryUncertain = false
	d.Next = time.Time{}
	w.runtime.SendGates = nil
	w.reconcileDeliveries(context.Background())
	if d.State == "stopped" || len(n.submissions) != 1 {
		t.Fatal("disabled should pause, not discard or send")
	}
	w.config.Options.RetryUncertain = true
	for i := 0; i < 2; i++ {
		d.Next = time.Time{}
		w.runtime.SendGates = nil
		w.reconcileDeliveries(context.Background())
	}
	if len(n.submissions) != 2 {
		t.Fatal("old unknown expired", d)
	}
}

type ownerReviewModel struct{ calls int }

func (m *ownerReviewModel) Chat(context.Context, string, []map[string]string) (string, error) {
	m.calls++
	return `{"action":"silent","messages":[]}`, nil
}
func TestOwnerReviewActuallyCallsModelForOrdinaryChatWithoutForcingReply(t *testing.T) {
	c := companion.Defaults()
	c.Enabled = true
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	c.Permissions.ObserveGroups = true
	c.Permissions.AutonomousGroups = true
	store, _ := companion.NewStore(t.TempDir())
	model := &ownerReviewModel{}
	engine, e := companion.New(c, store, model, "test")
	if e != nil {
		t.Fatal(e)
	}
	source := &fakeSource{messages: map[string]linuxnative.Message{}}
	sender := &fakeSender{}
	w, e := New(Config{StateDir: t.TempDir(), AllowedThreads: []string{"room@chatroom"}, GroupRules: map[string]GroupRule{"room@chatroom": {Mode: "social"}}}, source, engine, sender)
	if e != nil {
		t.Fatal(e)
	}
	_ = w.loadRuntime("server-1")
	for _, id := range []string{"first", "second"} {
		m := fixtureMessage(id, "room@chatroom", "大家生日快乐", "incoming")
		source.messages[id] = m
		ev := fixtureEvent(id, id, "room@chatroom", "incoming", "remote_sync")
		w.runtime.Inbox = append(w.runtime.Inbox, inboxItem{OwnerModelReview: true, Event: ev, Phase: "received"})
		w.handle(context.Background(), ev)
	}
	if model.calls != 2 || sender.calls != 0 {
		t.Fatal("review skipped model or forced a reply", model.calls, sender.calls)
	}
}
