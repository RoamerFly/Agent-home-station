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
	if !errors.Is(w.persistErr, companion.ErrDeliveryMemoryConflict) || p.Index != 0 || len(p.Sent) != 0 {
		t.Fatal("unverified draft bypassed conflict")
	}
}
