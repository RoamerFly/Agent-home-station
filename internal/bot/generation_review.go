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
		Updates []companion.ConversationEvent
	}{j.resourceKind, j.updates})
	return messageHash(string(b))
}
func (w *Worker) reviewActiveGenerations(ctx context.Context) {
	s := w.generation
	if s == nil {
		return
	}
	for _, j := range s.jobs {
		if j.cancelled || len(j.updates) == 0 || !j.engine.Config.ConversationConfig().RecheckOnMessage || time.Now().Before(j.nextReview) {
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
		pending := []string{"当前回复或工具任务尚在执行，尚未提交最终结果；资源类型：" + j.resourceKind + "。新请求另行处理。判断当前任务是否已被停止、改意或替代；无关聊天不取消。cancel表示停止本任务后续工作，不能撤回已经发生的外部操作。若cancel或replace，必须返回evidence_message_id为new_events中实际使原任务失效的消息编号，并给reason说明；不能仅因机器人没开机、催促、表情或新话题取消。"}
		modelCtx := w.generationContext(jobCtx, in)
		s.wg.Add(1)
		go func(j *generationJob, id, fingerprint string) {
			defer s.wg.Done()
			d, err := j.engine.ReviewPending(modelCtx, in, nil, pending, updates)
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
	if w.generation.jobs[j.input.Thread] != j || j.cancelled || generationFingerprint(j) != r.fingerprint {
		return
	}
	if r.err != nil {
		j.reviewFailures++
		if j.reviewFailures >= 2 {
			j.cancelled = true
			j.cancelReason = "generation_review_unavailable"
			j.cancel()
		}
		j.nextReview = time.Now().Add(5 * time.Second)
		return
	}
	j.reviewFailures = 0
	j.reviewed = r.fingerprint
	if r.decision.Action == "cancel" || r.decision.Action == "replace" {
		matched := false
		for _, event := range j.updates {
			if event.MessageID != "" && event.MessageID == r.decision.EvidenceMessageID {
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
		j.cancel()
		w.activity(Record{Event: "generation_revised", Category: "decision", ThreadID: j.input.Thread, MessageID: j.input.TraceID, Status: "stopped", Reason: j.cancelReason})
	}
}
