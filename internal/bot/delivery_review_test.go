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

func TestUnknownTextDeliveryDoesNotCallModelOrCreateReplacementIntent(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"replace","messages":["不应被用于投递重试"]}`}
	w, _, _, in := planWorker(t, model)
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w.sender = n
	_, _ = w.sendText(context.Background(), in, 0, "原始正文")
	d := w.runtime.Deliveries[0]
	for i := 0; i < 3; i++ {
		d.Next = time.Time{}
		w.runtime.SendGates = nil
		w.reconcileDeliveries(context.Background())
	}
	if model.calls != 0 || len(n.submissions) != 1 || d.Attempts != 0 || d.Text != "原始正文" || len(d.PreviousKeys) != 0 {
		t.Fatal("unknown outcome invoked model or created a second intent", model.calls, n.submissions, d)
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

func TestConfirmedRetryableTextFailureRetriesOriginalTextWithoutModel(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"replace","messages":["不应被用于投递重试"]}`}
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
	for i := 0; i < 2; i++ {
		d.Next = time.Time{}
		w.runtime.SendGates = nil
		w.reconcileDeliveries(context.Background())
	}
	if model.calls != 0 || len(n.submissions) != 2 || d.State != "accepted" || n.submissions[1].Text != "还有这条补充" || len(d.PreviousKeys) != 1 {
		t.Fatal("confirmed retryable failure did not retry the original text directly", model.calls, n.submissions, d)
	}
}

func TestCancelledTextDeliveryOnlyReconcilesExistingKey(t *testing.T) {
	w, _, _, in := planWorker(t, &planReviewModel{})
	in.TaskID = "text-task"
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w.sender = n
	active := true
	w.config.AgentTaskActive = func(companion.Input) bool { return active }
	_, _ = w.sendText(context.Background(), in, 0, "这条不该重发")
	d := w.runtime.Deliveries[0]
	active = false
	d.Next = time.Time{}
	w.runtime.SendGates = nil
	w.reconcileDeliveries(context.Background())
	if !d.Cancelled || d.State != "cancelled_reconciling" || len(n.submissions) != 1 || d.Attempts != 0 {
		t.Fatal("cancelled text created a new submission instead of entering receipt-only reconciliation", d, n.submissions)
	}
	status := n.intents[d.Key]
	status.State, status.ServerID = "accepted", "late-receipt"
	n.intents[d.Key] = status
	d.Next = time.Time{}
	w.reconcileDeliveries(context.Background())
	if d.State != "accepted" || len(n.submissions) != 1 {
		t.Fatal("late receipt was not recorded correctly after cancellation", d, n.submissions)
	}
}
