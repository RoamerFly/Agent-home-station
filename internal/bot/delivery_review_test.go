package bot

import (
	"context"
	"errors"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/sending"
)

func TestRetryModelKnowsFailureAndRewritesBeforeFreshSubmission(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"replace","messages":["生日歌已经唱好啦，祝寿星生日快乐～"]}`}
	w, _, _, in := planWorker(t, model)
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w.sender = n
	_, _ = w.sendText(context.Background(), in, 0, "来啦来啦，生日歌补上")
	d := w.runtime.Deliveries[0]
	for i := 0; i < 2; i++ {
		d.Next = time.Time{}
		w.runtime.SendGates = nil
		w.reconcileDeliveries(context.Background())
	}
	if model.calls != 1 || !d.RetryReviewReady || len(n.submissions) != 1 {
		t.Fatal("model was not consulted before resend", d, model.calls)
	}
	d.Next = time.Time{}
	w.runtime.SendGates = nil
	w.reconcileDeliveries(context.Background())
	if len(n.submissions) != 2 || n.submissions[1].Text != "生日歌已经唱好啦，祝寿星生日快乐～" || d.PreviousTexts[0] != "来啦来啦，生日歌补上" {
		t.Fatal("lost actual sent version", n.submissions, d)
	}
	// The old wording appearing in native sync must suppress another rewrite.
	n.rows = []map[string]any{{"direction": "outgoing", "body": "来啦来啦，生日歌补上", "serverId": "late", "createdAt": time.Now().Format(time.RFC3339)}}
	d.Next = time.Time{}
	w.reconcileDeliveries(context.Background())
	if d.State != "accepted" || d.Text != "来啦来啦，生日歌补上" || model.calls != 1 || len(n.submissions) != 2 {
		t.Fatal("late old wording ignored", d)
	}
}
func TestAcceptedVoiceAndFollowingTextSharePersistentGap(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	w.config.Options.RandomDelay = true
	w.config.Options.DelayMinSeconds = 3
	w.config.Options.DelayMaxSeconds = 8
	w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "song", Kind: "voice", Title: "song.wav"})
	n.status.State = "accepted"
	w.runtime.Media[0].Next = time.Time{}
	w.reconcileMedia(context.Background())
	gate := w.sendGate(in.Thread)
	if gate.Sub(w.runtime.Media[0].CompletedAt) < 3*time.Second {
		t.Fatal("no gap after accepted voice")
	}
	if e := w.loadRuntime("server-1"); e != nil {
		t.Fatal(e)
	}
	if !gate.Equal(w.sendGate(in.Thread)) {
		t.Fatal("restart lost gate")
	}
	textNative := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w.sender = textNative
	_, e := w.sendText(context.Background(), in, 0, "唱好啦")
	if !errors.Is(e, sending.ErrOutcomeUnknown) || len(textNative.submissions) != 0 || w.runtime.Deliveries[0].UncertainRounds != 0 {
		t.Fatal("text sent immediately or pacing treated as failed network", e)
	}
}

func TestModelCancelledRetryFinishesInboxWithoutSending(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"cancel","reason":"原请求者已明确取消旧补充"}`}
	w, c, n, in := planWorker(t, model)
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", in.TraceID, in.Thread, "incoming", "remote_sync"), Phase: "sending"}}
	d := &delivery{Thread: in.Thread, Trace: in.TraceID, Input: in, Text: "旧补充", State: "waiting", Created: time.Now()}
	w.runtime.Deliveries = append(w.runtime.Deliveries, d)
	w.reviewUncertainDelivery(context.Background(), c, d)
	if d.State != "stopped" || w.runtime.Inbox[0].Phase != "cancelled" || len(n.submissions) != 0 {
		t.Fatal("cancelled delivery remained pending or sent", d, w.runtime.Inbox)
	}
}

func TestNativeRetryableTextFailureIsReviewedByModelBeforeRetry(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"continue"}`}
	w, _, _, in := planWorker(t, model)
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}, acceptRetry: true}
	w.sender = n
	_, _ = w.sendText(context.Background(), in, 0, "还有这条补充")
	d := w.runtime.Deliveries[0]
	status := n.intents[d.Key]
	status.State = "failed"
	status.Retryable = true
	status.Reason = "transport_unavailable"
	n.intents[d.Key] = status
	for i := 0; i < 3; i++ {
		d.Next = time.Time{}
		w.runtime.SendGates = nil
		w.reconcileDeliveries(context.Background())
	}
	if model.calls != 1 || len(n.submissions) != 2 || d.State != "accepted" {
		t.Fatal("retryable failure skipped model or was abandoned", model.calls, d)
	}
}
