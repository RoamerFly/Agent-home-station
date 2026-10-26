package bot

import "wechat-bot/internal/companion"

// TaskDelivery describes delivery evidence separately from model completion.
type TaskDelivery struct {
	CaptionOmitted bool   `json:"caption_omitted,omitempty"`
	Trace          string `json:"trace"`
	State          string `json:"state"`
	Expected       int    `json:"expected"`
	Accepted       int    `json:"accepted"`
	AcceptedParts  int    `json:"accepted_parts"`
	TotalParts     int    `json:"total_parts"`
}

func (w *Worker) publishTaskDelivery(trace string) {
	if w.config.AgentTaskDelivery == nil || w.persistErr != nil {
		return
	}
	var input companion.Input
	result := TaskDelivery{Trace: trace, State: "waiting_delivery"}
	failed, unknown, pendingCaption := false, false, false
	for _, d := range w.runtime.Media {
		if d.Trace != trace || d.Input.TaskID == "" {
			continue
		}
		input = d.Input
		result.Expected++
		if d.State == "accepted" {
			result.Accepted++
		}
		failed = failed || d.State == "stopped" || d.UserCancelled
		unknown = unknown || d.State == "needs_review"
		for _, part := range d.Status.VoiceChunks {
			result.TotalParts++
			if part.State == "accepted" && part.HasReceipt {
				result.AcceptedParts++
			}
		}
	}
	if input.TaskID == "" || result.Expected == 0 {
		return
	}
	for _, r := range w.runtime.MediaReplies {
		if r.Trace != trace {
			continue
		}
		if r.Expected > result.Expected {
			result.Expected = r.Expected
		}
		failed = failed || r.State == "stopped"
		pendingCaption = pendingCaption || !r.terminal()
	}
	for _, p := range w.runtime.Plans {
		if p.Input.TraceID == trace {
			pendingCaption = pendingCaption || !p.terminal()
			omitted := p.MediaCaption && p.State == "stopped" && p.StopReason == "model_cancel"
			result.CaptionOmitted = result.CaptionOmitted || omitted
			failed = failed || p.State == "stopped" && !omitted
		}
	}
	switch {
	case unknown:
		result.State = "needs_review"
	case failed && result.Accepted > 0:
		result.State = "partial_failed"
	case failed:
		result.State = "failed"
	case result.Accepted == result.Expected && !pendingCaption:
		result.State = "accepted"
	case result.Accepted > 0 || result.AcceptedParts > 0:
		result.State = "partial"
	}
	if err := w.config.AgentTaskDelivery(input, result); err != nil {
		w.persistErr = err
	}
}
