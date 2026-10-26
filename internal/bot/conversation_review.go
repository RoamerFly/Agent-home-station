package bot

import (
	"context"
	"encoding/json"
	"time"
	"wechat-bot/internal/companion"
)

type planReviewResult struct {
	generation      *generationJob
	id, fingerprint string
	decision        companion.PendingReview
	err             error
}

func reviewFingerprint(p *utterancePlan) string {
	b, _ := json.Marshal(struct {
		Index, Slot    int
		Messages, Sent []string
		Updates        []companion.ConversationEvent
	}{p.Index, p.Slot, p.Messages, p.Sent, p.Updates})
	return messageHash(string(b))
}
func (w *Worker) startPlanReview(ctx context.Context, c *companion.Engine, p *utterancePlan) {
	s := w.generation
	if s.reviews[p.ID] != nil || s.occupied() >= 4 {
		return
	}
	jobCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	s.reviews[p.ID] = cancel
	id, fingerprint, in := p.ID, reviewFingerprint(p), p.Input
	sent, pending := append([]string(nil), p.Sent...), append([]string(nil), p.Messages[p.Index:]...)
	updates := append([]companion.ConversationEvent(nil), p.Updates...)
	modelCtx := w.generationContext(jobCtx, in)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		d, err := c.ReviewPending(modelCtx, in, sent, pending, updates)
		cancel()
		select {
		case s.reviewResults <- planReviewResult{id: id, fingerprint: fingerprint, decision: d, err: err}:
		case <-ctx.Done():
		}
	}()
}
func (w *Worker) finishPlanReview(r planReviewResult) {
	s := w.generation
	if cancel := s.reviews[r.id]; cancel != nil {
		cancel()
		delete(s.reviews, r.id)
	} else {
		return
	}
	if r.generation != nil {
		w.finishGenerationReview(r)
		return
	}
	for _, p := range w.runtime.Plans {
		if p.ID != r.id || p.terminal() {
			continue
		}
		if p.State != "review_pending" || reviewFingerprint(p) != r.fingerprint {
			// A second interruption arrived while the model reviewed the first.
			// Never apply that obsolete decision to the new unsent tail.
			return
		}
		w.applyPlanReview(p, r.decision, r.err)
		return
	}
}
func (w *Worker) applyPlanReview(p *utterancePlan, d companion.PendingReview, err error) {
	if err != nil {
		p.ReviewFailures++
		p.Next = time.Now().Add(5 * time.Second)
		if p.ReviewFailures >= 2 {
			p.State = "stopped"
		}
		w.persistErr = w.saveRuntime()
		w.activity(Record{Event: "utterance_review_failed", Category: "error", ThreadID: p.Input.Thread, MessageID: p.Input.TraceID, Status: p.State, Reason: "pending_plan_review_invalid_or_unavailable"})
		w.refreshMediaParent(p.Input.TraceID)
		return
	}
	p.ReviewFailures = 0
	p.Updates = nil
	switch d.Action {
	case "cancel":
		p.State = "stopped"
		p.StopReason = "model_cancel"
	case "replace":
		if p.Index+len(d.Messages) > 12 {
			p.State = "stopped"
			break
		}
		p.Messages = append(append([]string{}, p.Messages[:p.Index]...), d.Messages...)
		p.State = "queued"
	case "delay":
		p.Next = time.Now().Add(time.Duration(max(1, d.DelaySeconds)) * time.Second)
		p.State = "queued"
	case "continue":
		p.State = "queued"
	}
	w.persistErr = w.saveRuntime()
	w.activity(Record{Event: "utterance_plan_revised", Category: "decision", ThreadID: p.Input.Thread, MessageID: p.Input.TraceID, Status: p.State, Reason: "plan_" + d.Action})
	w.refreshMediaParent(p.Input.TraceID)
}
