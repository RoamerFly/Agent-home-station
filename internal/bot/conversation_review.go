package bot

import (
	"context"
	"encoding/json"
	"errors"
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
		ReviewReason   string
		Messages, Sent []string
		Updates        []companion.ConversationEvent
	}{p.Index, p.Slot, p.ReviewReason, p.Messages, p.Sent, p.Updates})
	return messageHash(string(b))
}
func (w *Worker) startPlanReview(ctx context.Context, c *companion.Engine, p *utterancePlan) {
	s := w.generation
	if s.reviews[p.ID] != nil || s.occupied() >= 4 {
		return
	}
	ctx, in, err := w.planReviewEvidence(ctx, c, p)
	if err != nil {
		w.applyPlanReview(p, companion.PendingReview{}, err)
		return
	}
	jobCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	s.reviews[p.ID] = cancel
	id, fingerprint := p.ID, reviewFingerprint(p)
	in.TraceID = p.Trace
	sent, pending := append([]string(nil), p.Sent...), append([]string(nil), p.Messages[p.Index:]...)
	updates := append([]companion.ConversationEvent(nil), p.Updates...)
	media := w.captionAttachments(p)
	isCaption := p.MediaCaption
	reviewReason := p.ReviewReason
	modelCtx := w.generationContext(jobCtx, in)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		var d companion.PendingReview
		var err error
		if reviewReason == "new_message" {
			d, err = c.ReviewInterruption(modelCtx, in, sent, pending, updates, media)
		} else if isCaption {
			d, err = c.ReviewMediaCaption(modelCtx, in, sent, pending, updates, media)
		} else {
			d, err = c.ReviewPending(modelCtx, in, sent, pending, updates)
		}
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
	if err == nil && d.Action != "cancel" && p.ReviewReason != "new_message" {
		if evidence, ok := d.GenerationEvidence(); ok {
			c, available := w.model.(*companion.Engine)
			if !available {
				err = errors.New("pending_review_evidence_unavailable")
			} else {
				var scoped *companion.Engine
				scoped, err = c.ForEvidenceInput(p.Input)
				if err == nil {
					err = scoped.ValidateGenerationEvidence(context.Background(), evidence)
				}
			}
			if err == nil {
				p.ReviewEvidence = &evidence
			}
		} else {
			err = errors.New("pending_review_evidence_unavailable")
		}
	}
	if err == nil && d.Action == "cancel" && p.Input.IsGroup && p.Input.TaskID != "" {
		// Another member may correct a conversation, but cannot revoke the
		// original requester's persistent task through a quoted interruption.
		authorized := false
		for _, event := range p.Updates {
			if event.Sender == p.Input.Sender && (d.EvidenceMessageID == "" || event.MessageID == d.EvidenceMessageID) {
				authorized = true
			}
		}
		if !authorized {
			d.Action, d.Reason = "continue", "other_member_cannot_cancel_task"
		}
	}
	if p.ReviewReason == "new_message" {
		p.finishUpdateCheck()
		returnState := p.ReviewReturnState
		if returnState == "" {
			returnState = "queued"
		}
		p.ReviewReason, p.ReviewReturnState = "", ""
		p.ReviewFailures = 0
		if err != nil {
			// A failed one-time judgment does not hold the old reply in a loop.
			// Submission still validates its original delivery evidence.
			p.State = returnState
			if returnState == "accepted" && p.WakePending {
				if c, ok := w.model.(*companion.Engine); ok {
					w.scheduleConversationWake(c, p.Input, p.WakeAfterSeconds, p.WaitForReply, p.WakeReason)
				}
				p.WakePending = false
			}
			w.persistErr = w.saveRuntime()
			w.activity(Record{Event: "utterance_interruption_check_failed", Category: "decision", ThreadID: p.Input.Thread, MessageID: p.Input.TraceID, Status: p.State, Reason: "original_plan_continues"})
			return
		}
		if d.Action == "cancel" && (p.Index < len(p.Messages) || len(d.RecallSentIndices) > 0) {
			p.State, p.StopReason = "stopped", "explicit_correction_cancelled"
			p.WakePending = false
			seen := map[int]bool{}
			for _, index := range d.RecallSentIndices {
				if index >= 0 && index < p.Index && !seen[index] {
					p.PendingRecall = append(p.PendingRecall, index)
					seen[index] = true
				}
			}
			p.RecallNext = time.Now()
		} else {
			p.State = returnState
			if returnState == "accepted" && p.WakePending {
				if c, ok := w.model.(*companion.Engine); ok {
					w.scheduleConversationWake(c, p.Input, p.WakeAfterSeconds, p.WaitForReply, p.WakeReason)
				}
				p.WakePending = false
			}
		}
		w.persistErr = w.saveRuntime()
		w.activity(Record{Event: "utterance_interruption_checked", Category: "decision", ThreadID: p.Input.Thread, MessageID: p.Input.TraceID, Status: p.State, Reason: "correction_" + d.Action, Content: d.Reason})
		w.refreshMediaParent(p.Input.TraceID)
		return
	}
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
	p.EpochReplan = false
	p.finishUpdateCheck()
	switch d.Action {
	case "cancel":
		p.State = "stopped"
		p.StopReason = "model_cancel"
	case "replace":
		p.Messages = append(append([]string{}, p.Messages[:p.Index]...), d.Messages...)
		p.ReplyContexts = nil // Rewritten text must be grounded again before submission.
		p.Input.ReplyContext = nil
		p.State = "queued"
	case "delay":
		if len(d.Messages) > 0 {
			p.Messages = append(append([]string{}, p.Messages[:p.Index]...), d.Messages...)
			p.ReplyContexts = nil
			p.Input.ReplyContext = nil
		}
		p.Next = time.Now().Add(time.Duration(max(1, d.DelaySeconds)) * time.Second)
		p.State = "queued"
	case "continue":
		p.State = "queued"
	}
	w.persistErr = w.saveRuntime()
	w.activity(Record{Event: "utterance_plan_revised", Category: "decision", ThreadID: p.Input.Thread, MessageID: p.Input.TraceID, Status: p.State, Reason: "plan_" + d.Action, Content: d.Reason})
	w.refreshMediaParent(p.Input.TraceID)
}

func (w *Worker) captionAttachments(p *utterancePlan) []companion.DeliveredAttachment {
	items := []companion.DeliveredAttachment{}
	for _, d := range w.runtime.Media {
		if d.Trace == p.Trace && d.Input.Thread == p.Input.Thread && d.State == "accepted" {
			items = append(items, companion.DeliveredAttachment{Kind: d.Kind, Title: d.Title, State: d.State})
		}
	}
	return items
}

func (w *Worker) reviewPlan(ctx context.Context, c *companion.Engine, p *utterancePlan) (companion.PendingReview, error) {
	ctx, in, err := w.planReviewEvidence(ctx, c, p)
	if err != nil {
		return companion.PendingReview{}, err
	}
	in.TraceID = p.Trace
	if p.ReviewReason == "new_message" {
		return c.ReviewInterruption(ctx, in, p.Sent, p.Messages[p.Index:], p.Updates, w.captionAttachments(p))
	}
	if p.MediaCaption {
		return c.ReviewMediaCaption(ctx, in, p.Sent, p.Messages[p.Index:], p.Updates, w.captionAttachments(p))
	}
	return c.ReviewPending(ctx, in, p.Sent, p.Messages[p.Index:], p.Updates)
}

func (w *Worker) planReviewEvidence(ctx context.Context, c *companion.Engine, p *utterancePlan) (context.Context, companion.Input, error) {
	in := p.Input
	in.TraceID = p.Trace
	if p.Index < len(p.ReplyContexts) {
		r := p.ReplyContexts[p.Index]
		in.ReplyContext = &r
	}
	if p.ReviewReason == "new_message" {
		// Judge the correction from the current authorized source. The proof
		// for the old text belongs to the later submission, not this read.
		in.ReplyContext = nil
	}
	reader, err := w.deliveryEvidenceReader(in)
	if err != nil {
		return ctx, in, err
	}
	if scoped, ok := reader.model.(*companion.Engine); ok {
		c = scoped
	}
	if p.ReviewEvidence != nil && p.ReviewReason != "new_message" {
		if err := c.ValidateGenerationEvidence(ctx, *p.ReviewEvidence); err != nil {
			return ctx, in, err
		}
		ctx = companion.WithStoredGenerationEvidence(ctx, *p.ReviewEvidence)
	}
	if !in.IsGroup && (in.ReplyContext == nil || reader.bindingEvidenceMissing(in) || replyProofMetadataMissing(in.ReplyContext)) && p.ReviewEvidence == nil && !p.EpochReplan && p.ReviewReason != "new_message" {
		return ctx, in, errors.New("private_reply_requires_replan")
	}
	return ctx, in, nil
}
