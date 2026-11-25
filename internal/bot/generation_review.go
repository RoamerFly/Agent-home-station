package bot

import (
	"context"
	"encoding/json"
	"time"
	"wechat-bot/internal/companion"
)

func generationFingerprint(j *generationJob) string {
	b, _ := json.Marshal(struct {
		Kind    string
		Request string
		Updates []companion.ConversationEvent
	}{j.resourceKind, j.input.Text, j.updates})
	return messageHash(string(b))
}
func (w *Worker) reviewActiveGenerations(ctx context.Context) {
	s := w.generation
	if s == nil {
		return
	}
	for _, j := range s.jobs {
		if j.session != nil || j.cancelled || j.observationPending || len(j.updates) == 0 || !j.engine.Config.ConversationConfig().RecheckOnMessage || time.Now().Before(j.nextReview) {
			continue
		}
		id, fingerprint := "generation-"+j.input.TraceID, generationFingerprint(j)
		if j.reviewed == fingerprint || s.reviews[id] != nil || s.occupied() >= 4 {
			continue
		}
		jobCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		s.reviews[id] = cancel
		in := j.input
		updates := append([]companion.ConversationEvent(nil), j.updates...)
		pending := []string{"原任务仍在执行；资源类型：" + j.resourceKind + "。只判断原请求者的明确纠错是否要求停止旧任务。"}
		modelCtx := w.generationContext(jobCtx, in)
		s.wg.Add(1)
		go func(j *generationJob, id, fingerprint string) {
			defer s.wg.Done()
			d, err := j.engine.ReviewGeneration(modelCtx, in, pending, updates)
			cancel()
			select {
			case s.reviewResults <- planReviewResult{generation: j, id: id, fingerprint: fingerprint, decision: d, err: err}:
			case <-ctx.Done():
			}
		}(j, id, fingerprint)
	}
}
func (w *Worker) finishGenerationReview(r planReviewResult) {
	j := r.generation
	if w.generation.jobs[j.input.Thread] != j || j.cancelled || j.observationPending || generationFingerprint(j) != r.fingerprint {
		return
	}
	if r.err != nil {
		j.reviewed = r.fingerprint
		w.activity(Record{Event: "generation_review_retained", Category: "decision", ThreadID: j.input.Thread, MessageID: j.input.TraceID, Status: "processing", Reason: "correction_check_unavailable"})
		return
	}
	j.reviewFailures = 0
	if r.decision.Action == "delay" {
		j.reviewed = r.fingerprint
		return
	}
	j.reviewed = r.fingerprint
	if r.decision.Action == "cancel" {
		matched := false
		for _, event := range j.updates {
			if event.MessageID != "" && event.MessageID == r.decision.EvidenceMessageID && (j.input.Sender == "" || event.Sender == j.input.Sender) {
				matched = true
				break
			}
		}
		if !matched || r.decision.Reason == "" {
			w.activity(Record{Event: "generation_review_retained", Category: "decision", ThreadID: j.input.Thread, MessageID: j.input.TraceID, Status: "processing", Reason: "cancellation_without_message_evidence"})
			return
		}
		j.cancelled = true
		j.cancelReason = "model_cancelled_superseded_task"
		j.cancelEvidence = r.decision.EvidenceMessageID
		j.cancelNote = r.decision.Reason
		j.cancel()
		w.activity(Record{Event: "generation_revised", Category: "decision", ThreadID: j.input.Thread, MessageID: j.input.TraceID, Status: "stopped", Reason: j.cancelReason, RelatedMessageID: j.cancelEvidence, DecisionNote: j.cancelNote})
	}
}
