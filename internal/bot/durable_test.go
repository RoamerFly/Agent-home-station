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
func TestUnknownDeliveryOnlyReconcilesSameKeyAcrossRestart(t *testing.T) {
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
	if d.Attempts != 0 || len(n.submissions) != 1 || d.Reason != "delivery_receipt_unverified" {
		t.Fatal("unknown receipt created a retry intent", d, n.submissions)
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
	if len(n.submissions) != 1 || d.Attempts != 0 {
		t.Fatal("restart created a retry for an unknown receipt", len(n.submissions), d)
	}
	d.Next = time.Time{}
	w.runtime.SendGates = nil
	w2.runtime.SendGates = nil
	w2.reconcileDeliveries(context.Background())
	if len(n.submissions) != 1 || d.State == "stopped" || d.Attempts != 0 {
		t.Fatal("unknown receipt was stopped or resent")
	}
	if len(n.submissions) != 1 || d.Attempts != 0 || len(d.PreviousKeys) != 0 || d.UncertainRounds < 1 {
		t.Fatal("unknown receipt was not retained as a lookup-only delivery", d)
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
	if d.State != "accepted" || len(n.submissions) != 1 {
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
func TestUncertainDeliveryPausesAfterFiniteChecksWithoutResending(t *testing.T) {
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w := durableWorker(t, n)
	_, _ = w.sendText(context.Background(), companion.Input{Thread: "filehelper", TraceID: "older"}, 0, "待核对文字")
	d := w.runtime.Deliveries[0]
	key := d.Key
	d.Created = time.Now().Add(-48 * time.Hour)
	w.config.Options.RetryUncertain = false
	for i := 0; i < 8 && d.State != "awaiting_evidence"; i++ {
		d.Next = time.Time{}
		w.runtime.SendGates = nil
		w.reconcileDeliveries(context.Background())
	}
	if len(n.submissions) != 1 || d.Attempts != 0 || d.Key != key || !d.Next.IsZero() || d.UncertainRounds != 6 {
		t.Fatal("unknown receipt lost its original intent or kept a retry timer", d)
	}
}

func TestLegacyUncertainDeliveryParksOnUpgradeWithoutNewMessageWake(t *testing.T) {
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w := durableWorker(t, n)
	in := companion.Input{Thread: "room@chatroom", TraceID: "original", Mode: "direct"}
	d := &delivery{Trace: in.TraceID, Thread: in.Thread, Input: in, Bubble: 0, Text: "原发送内容", Key: "original-key", State: "waiting", Created: time.Now().Add(-time.Hour), Next: time.Now().Add(time.Hour), UncertainRounds: 26}
	w.runtime.Deliveries = append(w.runtime.Deliveries, d)
	if err := w.saveRuntime(); err != nil {
		t.Fatal(err)
	}
	var records []Record
	w.config.Record = func(r Record) { records = append(records, r) }
	w.reconcileDeliveries(t.Context())
	w.reconcileDeliveries(t.Context())
	if d.State != "awaiting_evidence" || !d.Next.IsZero() || d.Key != "original-key" || len(records) != 1 || records[0].Event != "delivery_receipt_paused" || len(n.submissions) != 0 {
		t.Fatal("legacy receipt loop did not park once without resending", d, records)
	}
	w.recordIncoming(linuxnative.Message{ID: "new-message", ThreadID: in.Thread, CreatedAt: time.Now().Format(time.RFC3339)})
	w.reconcileDeliveries(t.Context())
	if d.State != "awaiting_evidence" || !d.Next.IsZero() || len(n.submissions) != 0 || len(records) != 2 {
		t.Fatal("ordinary message restarted an old receipt loop", d, records)
	}
	n.intents[d.Key] = linuxnative.ToolStatus{ID: "original-job", ThreadID: in.Thread, State: "accepted", ServerID: "late-receipt"}
	w.reconcileDeliveries(t.Context())
	if d.State != "awaiting_evidence" || d.Key != "original-key" || len(n.submissions) != 0 {
		t.Fatal("parked intent was altered without explicit reconciliation", d)
	}
	outgoing := linuxnative.Message{Direction: "outgoing", ThreadID: in.Thread, Body: d.Text, ServerID: "another-message", CreatedAt: time.Now().Format(time.RFC3339)}
	w.reconcilePausedOutgoing(t.Context(), outgoing)
	if d.State != "awaiting_evidence" {
		t.Fatal("equal text without the original receipt was accepted", d)
	}
	outgoing.ServerID = "late-receipt"
	d.ServerID = "late-receipt"
	n.intents[d.Key] = linuxnative.ToolStatus{ID: "original-job", ThreadID: in.Thread, State: "needs_review", ServerID: "late-receipt"}
	w.reconcilePausedOutgoing(t.Context(), outgoing)
	if d.State != "awaiting_evidence" {
		t.Fatal("matching server ID bypassed the original key status", d)
	}
	n.intents[d.Key] = linuxnative.ToolStatus{ID: "original-job", ThreadID: in.Thread, State: "accepted", ServerID: "late-receipt"}
	w.reconcilePausedOutgoing(t.Context(), outgoing)
	if d.State != "accepted" || d.ID != "original-job" || d.ServerID != "late-receipt" || len(n.submissions) != 0 {
		t.Fatal("matching native receipt did not settle parked intent", d)
	}
}

func TestLateAcceptedReceiptResumesExplicitCorrectionOnce(t *testing.T) {
	w, c, _, in := planWorker(t, &planReviewModel{})
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w.sender = n
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"原回复"}})
	p := w.runtime.Plans[0]
	p.State, p.StopReason, p.ReviewReason = "stopped", "correction_receipt_awaiting_evidence", "new_message"
	p.InboxChecked = true
	d := &delivery{Trace: in.TraceID, Thread: in.Thread, Input: in, Bubble: p.Slot, Text: "原回复", SubmittedText: "原回复", Key: "original-key", ID: "original-job", ServerID: "server-1", State: "awaiting_evidence", Created: time.Now()}
	w.runtime.Deliveries = append(w.runtime.Deliveries, d)
	w.acceptReconciled(d)
	if d.State != "accepted" || p.State != "review_pending" || p.StopReason != "" || p.ReviewReason != "new_message" || p.ReviewReturnState != "accepted" || p.Index != 1 || p.Slot != 1 || len(p.Sent) != 1 || p.Sent[0] != "原回复" || !p.WakePending || len(n.submissions) != 0 {
		t.Fatal("late accepted text did not reopen exactly one correction decision", d.State, p)
	}
	w.resumeCorrectionAfterReceipt(p, d)
	if p.Index != 1 || len(p.Sent) != 1 || len(n.submissions) != 0 {
		t.Fatal("late receipt resumed correction twice or resent text", p)
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
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
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

func TestConsecutiveOrdinaryMessagesCallModelWithoutAnyCooldown(t *testing.T) {
	c := companion.Defaults()
	c.Enabled = true
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	c.Permissions.ObserveGroups = true
	c.Permissions.AutonomousGroups = true
	store, _ := companion.NewStore(t.TempDir())
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
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
		w.runtime.Inbox = append(w.runtime.Inbox, inboxItem{Event: ev, Phase: "received"})
		w.handle(context.Background(), ev)
	}
	if model.calls != 2 || sender.calls != 0 {
		t.Fatal("review skipped model or forced a reply", model.calls, sender.calls)
	}
}

func TestWrongConversationReceiptCannotSettleOrResend(t *testing.T) {
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w := durableWorker(t, n)
	_, err := w.sendText(t.Context(), companion.Input{Thread: "filehelper", TraceID: "source", Directed: true, Mode: "direct"}, 0, "合成回执测试")
	if !errors.Is(err, sending.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	d := w.runtime.Deliveries[0]
	n.intents[d.Key] = linuxnative.ToolStatus{ID: d.ID, ThreadID: "other", Kind: "text", State: "accepted", ServerID: "wrong"}
	d.Next = time.Time{}
	w.reconcileDeliveries(t.Context())
	if d.State == "accepted" || d.ServerID == "wrong" || !d.Next.After(time.Now()) || len(n.submissions) != 1 {
		t.Fatalf("wrong receipt settled or resubmitted: %+v", d)
	}
	n.intents[d.Key] = linuxnative.ToolStatus{ID: d.ID, ThreadID: "filehelper", Kind: "text", State: "accepted", ServerID: "right"}
	d.Next = time.Time{}
	w.reconcileDeliveries(t.Context())
	if d.State != "accepted" || d.ServerID != "right" || len(n.submissions) != 1 {
		t.Fatal("late correct receipt lost", d)
	}
}

func TestSegmentedReplyOnlyQuotesFirstAcrossRestart(t *testing.T) {
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}, acceptRetry: true}
	w := durableWorker(t, n)
	in := companion.Input{Thread: "room@chatroom", TraceID: "synthetic-segments", QuoteServerID: "77", QuoteSharedMessage: true, Mode: "social"}
	for bubble := 0; bubble < 3; bubble++ {
		w.runtime.SendGates = nil
		_, _ = w.sendText(context.Background(), in, bubble, "合成回复")
		if len(n.submissions) != bubble+1 {
			t.Fatalf("bubble %d missing", bubble)
		}
		if (n.submissions[bubble].Quote != nil) != (bubble == 0) {
			t.Fatalf("wrong quote for bubble %d", bubble)
		}
		if err := w.loadRuntime("server-1"); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = w.sendText(context.Background(), in, 0, "合成回复")
	if len(n.submissions) != 3 {
		t.Fatal("restart duplicated first segment")
	}
}

func TestUndirectedGroupMediaReplyQuotesOnlyFirstProgressAndSegment(t *testing.T) {
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}, acceptRetry: true}
	w := durableWorker(t, n)
	in := companion.Input{Thread: "room@chatroom", TraceID: "original-image", QuoteServerID: "native-server-77", Mode: "social", IsGroup: true}
	for index, bubble := range []int{-1, 0, 1} {
		w.runtime.SendGates = nil
		_, _ = w.sendText(context.Background(), in, bubble, "合成回复")
		if len(n.submissions) != index+1 {
			t.Fatalf("bubble %d missing: %d submissions", bubble, len(n.submissions))
		}
		quote := n.submissions[index].Quote
		if index == 0 && (quote == nil || quote.MessageID != in.TraceID || quote.ServerID != in.QuoteServerID) {
			t.Fatalf("first reply did not quote original: %+v", quote)
		}
		if index > 0 && quote != nil {
			t.Fatalf("bubble %d repeated original quote: %+v", bubble, quote)
		}
		if err := w.loadRuntime("server-1"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnsubmittedCancelledProgressDoesNotConsumeOriginalQuote(t *testing.T) {
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w := durableWorker(t, n)
	in := companion.Input{Thread: "room@chatroom", TraceID: "original-image", QuoteServerID: "native-server-77", Mode: "social", IsGroup: true}
	w.runtime.Deliveries = append(w.runtime.Deliveries, &delivery{Thread: in.Thread, Trace: in.TraceID, Bubble: -1, Input: in, Quote: replyQuote(in), State: "stopped"})
	_, _ = w.sendText(context.Background(), in, 0, "第一条实际提交的回复")
	if len(n.submissions) != 1 || n.submissions[0].Quote == nil || n.submissions[0].Quote.MessageID != in.TraceID {
		t.Fatalf("cancelled unsent progress consumed quote: %+v", n.submissions)
	}
}
