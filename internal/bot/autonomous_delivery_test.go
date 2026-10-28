package bot

import (
	"context"
	"errors"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type brokenReconciler struct{ *mediaFake }

func (n *brokenReconciler) ReconcileMediaJob(context.Context, string) (linuxnative.ToolStatus, error) {
	n.retries++
	return n.status, errors.New("repair required")
}
func TestFailedReconciliationReportsOnceAndStillReadsLateReceiptDuringBackoff(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "source", Kind: "voice", Title: "song.wav"})
	d := w.runtime.Media[0]
	n.status.State = "needs_review"
	n.status.Reason = "media_processing_failed"
	d.Checks = 1
	d.Next = time.Time{}
	w.sender = &brokenReconciler{n}
	w.runtime.SendGates = nil
	reports := 0
	w.config.MediaRepairRequired = func(got companion.Input, job, reason string) error {
		reports++
		if job != d.ID || reason != "media_processing_failed" {
			t.Fatal(job, reason)
		}
		return nil
	}
	w.reconcileMedia(context.Background())
	if reports != 1 || n.retries != 1 || d.ReconcileAfter.Before(time.Now()) || d.Next.After(time.Now().Add(16*time.Second)) {
		t.Fatal("failure hidden or receipt reads parked", d, reports, n.retries)
	}
	d.Next = time.Time{}
	w.reconcileMedia(context.Background())
	if reports != 1 || n.retries != 1 {
		t.Fatal("repeated repair or ignored backoff", reports, n.retries)
	}
	n.status.State = "accepted"
	n.status.ServerID = "late-receipt"
	d.Next = time.Time{}
	w.reconcileMedia(context.Background())
	if d.State != "accepted" || n.sends != 1 || n.retries != 1 {
		t.Fatal("late receipt missed or duplicate sent", d, n.sends, n.retries)
	}
}

type recoveringMediaNative struct{ *mediaFake }

func (n *recoveringMediaNative) ReconcileMediaJob(context.Context, string) (linuxnative.ToolStatus, error) {
	n.retries++
	n.status.State = "queued"
	return n.status, nil
}
func TestUnknownMediaResumesAfterManyAttemptsWithoutResubmittingAcceptedParts(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	w.queueMedia(context.Background(), c, in, companion.GeneratedMedia{ID: "source", Kind: "voice", Title: "song.wav"})
	d := w.runtime.Media[0]
	n.status.State = "needs_review"
	n.status.Retryable = false
	n.status.VoiceChunks = []linuxnative.VoiceChunkStatus{{Index: 0, State: "accepted", HasReceipt: true, ServerID: "123"}, {Index: 1, State: "needs_review"}}
	d.State = "waiting"
	d.Checks = 41
	d.Retries = 40
	d.Next = time.Time{}
	n.retries = 40
	w.runtime.SendGates = nil
	w.sender = &recoveringMediaNative{n}
	w.reconcileMedia(context.Background())
	if n.sends != 1 || n.retries != 41 || d.State != "queued" || !d.Status.VoiceChunks[0].HasReceipt {
		t.Fatal("stopped at legacy quota or re-created accepted media", n.sends, n.retries, d)
	}
}
