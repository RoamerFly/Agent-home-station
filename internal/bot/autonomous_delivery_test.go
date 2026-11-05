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

func TestAgentChecksOnlyOriginalReceiptAcrossRestartWhenResendDisabled(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	w.config.Options.RetryUncertain = false
	ctx := t.Context()
	w.queueMedia(ctx, c, in, companion.GeneratedMedia{ID: "source", Kind: "voice", Title: "test.wav"})
	d := w.runtime.Media[0]
	n.status.State = "needs_review"
	d.Checks, d.Next = 3, time.Time{}
	w.reconcileMedia(ctx)
	if d.State != "checking_receipt" || !d.Next.After(time.Now()) {
		t.Fatal(d)
	}
	due := d.Next
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	d = w.runtime.Media[0]
	reads := 0
	n.onStatus = func() { reads++ }
	w.reconcileMedia(ctx)
	if reads != 0 || !d.Next.Equal(due) {
		t.Fatal("restart bypassed backoff")
	}
	d.Next = time.Time{}
	n.status.State, n.status.ServerID = "accepted", "synthetic-receipt"
	w.reconcileMedia(ctx)
	if d.State != "accepted" || reads != 1 || n.sends != 1 || n.retries+n.resumes != 0 {
		t.Fatal(d.State, reads, n.sends, n.retries)
	}
}

func TestMissingReceiptKeyCannotResubmitAfterCrash(t *testing.T) {
	w, n, _, in := mediaWorker(t)
	w.config.Options.RetryUncertain = false
	d := &mediaDelivery{Key: "original-key", Trace: in.TraceID, Input: in, Kind: "voice", State: "submitting", Created: time.Now().Add(-48 * time.Hour)}
	w.runtime.Media = []*mediaDelivery{d}
	for i := 0; i < 3; i++ {
		d.Next = time.Time{}
		w.reconcileMedia(t.Context())
		if d.State != "checking_receipt" || !d.Next.After(time.Now()) || n.sends+n.retries+n.resumes != 0 {
			t.Fatal("missing key authorized a write", d.State)
		}
	}
	n.keys[d.Key] = true
	n.status = linuxnative.ToolStatus{ID: "other", ThreadID: "other-conversation", State: "accepted", ServerID: "wrong"}
	d.Next = time.Time{}
	w.reconcileMedia(t.Context())
	if d.State != "needs_review" || d.Reason != "media_identity_mismatch" || n.sends != 0 {
		t.Fatal("foreign receipt accepted", d.State, d.Reason)
	}
}

func TestLegacyRecoveryOpensOnlyReadOnlyMediaReceipts(t *testing.T) {
	w, n, _, in := mediaWorker(t)
	w.config.Options.RetryUncertain = true
	const reason = "media_receipt_unverified_native_retry_denied"
	for _, d := range []mediaDelivery{
		{ID: "pending", Reason: reason}, {Key: "submitted", Reason: "media_status_unavailable_after_deadline"},
		{ID: "cancelled", Reason: reason, UserCancelled: true}, {ID: "paused", Reason: reason, Paused: true},
		{ID: "invalid", Reason: "media_source_proof_invalid"}, {Reason: reason},
	} {
		d.Trace, d.Input, d.State = "original", in, "needs_review"
		w.runtime.Media = append(w.runtime.Media, &d)
	}
	if err := w.saveRuntime(); err != nil {
		t.Fatal(err)
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	for i, d := range w.runtime.Media {
		if (d.State == "checking_receipt") != (i < 2) {
			t.Fatal("wrong migration", i, d.State)
		}
	}
	if n.sends+n.retries+n.resumes != 0 {
		t.Fatal("migration wrote to native")
	}
}

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
