package bot

import (
	"context"
	"errors"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestReceiptMemoryRecoversLegacyCollisionWithoutSendingOrReplacing(t *testing.T) {
	w, c, n, in := planWorker(t, &planReviewModel{})
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"本次已接受的回复"}})
	p := w.runtime.Plans[0]
	if err := c.Store.AcceptedDelivery(in, "迁移保留的旧回复", p.ID+"-0", c.Config, time.Now().Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.sendText(context.Background(), in, 0, "本次已接受的回复"); err != nil {
		t.Fatal(err)
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if w.persistErr != nil || len(n.submissions) != 1 || w.runtime.Plans[0].State != "accepted" {
		t.Fatal("recovery failed", w.persistErr, len(n.submissions))
	}
	// Recreate the crash point after memory commit but before queue advancement.
	p = w.runtime.Plans[0]
	p.Index = 0
	p.Slot = 0
	p.Sent = nil
	p.State = "waiting_receipt"
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	counts := map[string]int{}
	for _, turn := range c.Store.Context(in, c.Config, time.Now()).Messages {
		if turn.Role == "assistant" {
			counts[turn.Text]++
		}
	}
	if w.persistErr != nil || len(n.submissions) != 1 || counts["迁移保留的旧回复"] != 1 || counts["本次已接受的回复"] != 1 {
		t.Fatal("not idempotent", counts, w.persistErr)
	}
	if err := c.Store.AcceptedDelivery(in, "直接覆盖旧内容", p.ID+"-0", c.Config, time.Now()); !errors.Is(err, companion.ErrDeliveryMemoryConflict) {
		t.Fatal("legacy conflict protection removed", err)
	}
}

func TestMemoryCollisionWithoutAcceptedReceiptStillFailsClosed(t *testing.T) {
	w, c, _, in := planWorker(t, &planReviewModel{})
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"新草稿"}})
	p := w.runtime.Plans[0]
	if err := c.Store.AcceptedDelivery(in, "原文", p.ID+"-0", c.Config, time.Now()); err != nil {
		t.Fatal(err)
	}
	w.acceptPlanBubble(c, p, "新草稿")
	if !errors.Is(w.persistErr, companion.ErrDeliveryIdentityRequired) || p.Index != 0 || len(p.Sent) != 0 {
		t.Fatal("unverified draft bypassed conflict")
	}
}

func TestAcceptedTextMemoryUsesReceiptAndRejectsMismatchedBody(t *testing.T) {
	w, c, _, in := planWorker(t, &planReviewModel{})
	if _, err := w.sendText(context.Background(), in, 0, "已确认正文"); err != nil {
		t.Fatal(err)
	}
	if err := w.acceptTextMemory(c, in, 0, "不同正文"); !errors.Is(err, companion.ErrDeliveryMemoryConflict) {
		t.Fatalf("mismatched accepted body = %v, want conflict", err)
	}
	if err := w.acceptTextMemory(c, in, 0, "已确认正文"); err != nil {
		t.Fatal(err)
	}
	scope := c.Store.Context(in, c.Config, time.Now())
	for _, turn := range scope.Messages {
		if turn.Role == "assistant" && turn.Text == "已确认正文" {
			if turn.DeliveryID == "" || turn.RequestTraceID != in.TraceID {
				t.Fatalf("receipt/request identity missing: %#v", turn)
			}
			return
		}
	}
	t.Fatal("accepted bubble was not retained")
}

func TestDeliveryAcceptedTimePrefersObservedOutgoingTimestamp(t *testing.T) {
	fallback := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	want := fallback.Add(-4 * time.Minute)
	got := deliveryAcceptedTime(&delivery{AcceptedAt: want.Format(time.RFC3339Nano)}, fallback)
	if !got.Equal(want) {
		t.Fatalf("accepted time = %s, want receipt time %s", got, want)
	}
	if got = deliveryAcceptedTime(&delivery{AcceptedAt: "not-a-time"}, fallback); !got.Equal(fallback) {
		t.Fatalf("invalid receipt time = %s, want fallback %s", got, fallback)
	}
}

func TestLegacyReceiptMemoryKeyRequiresCompleteNativeProof(t *testing.T) {
	stamp := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	d := &delivery{Key: "native-idempotency", ID: "native-job", Created: stamp}
	if got := legacyReceiptMemoryKey(d); got == "" || got[:9] != "delivery-" {
		t.Fatalf("legacy receipt key = %q", got)
	}
	for _, incomplete := range []*delivery{{Key: d.Key, Created: stamp}, {ID: d.ID, Created: stamp}, {Key: d.Key, ID: d.ID}} {
		if got := legacyReceiptMemoryKey(incomplete); got != "" {
			t.Fatalf("incomplete delivery manufactured legacy key %q from %#v", got, incomplete)
		}
	}
}
