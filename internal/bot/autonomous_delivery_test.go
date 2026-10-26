package bot

import (
	"context"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

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
