package bot

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

func queueTool() mcptools.Tool {
	return mcptools.Tool{Name: "wechat_queue_control", Description: "Inspect/pause/resume/cancel retained attachment queues in THIS conversation. Ordinary chat never changes a queue. pause/resume affects only an unsubmitted or safely paused queue. A user-cancelled segmented voice job requires trace_id and a new explicit resume instruction: it may resume only verified missing segments under a NEW native job, never replay accepted segments; if any segment is in-flight or unverified, report the boundary instead. trace_id selects one original turn; omitted selects queues belonging to current sender plus autonomous shares. The real owner may manage all queues here. Cancel removes pending content, not already delivered messages; recall is separate. Input {action:list|pause|resume|cancel,trace_id?:string}.", InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["list","pause","resume","cancel"]},"trace_id":{"type":"string"}},"required":["action"],"additionalProperties":false}`)}
}

func queueOwned(in, old companion.Input, owner bool) bool {
	return in.Thread == old.Thread && (owner || old.Sender == in.Sender || old.Surf)
}

// resumeMissingMediaNative deliberately has a separate operation from retry.
// Retry can legitimately replay a complete job after a transient provider
// failure; it is unsafe after a user has cancelled a segmented voice send.
type resumeMissingMediaNative interface {
	ResumeMissingMediaJob(context.Context, string, string) (linuxnative.ToolStatus, error)
}

func resumableVoiceChunks(d *mediaDelivery) bool {
	if d.Kind != "voice" || d.ID == "" || d.Status.VoicePartsIncomplete || len(d.Status.VoiceChunks) == 0 {
		return false
	}
	missing := false
	for _, chunk := range d.Status.VoiceChunks {
		switch chunk.State {
		case "accepted":
			if !chunk.HasReceipt || chunk.ServerID == "" {
				return false
			}
		case "cancelled", "prepared":
			missing = true
		default: // includes queued, running, submitting, unknown and failed
			return false
		}
	}
	return missing
}

func (w *Worker) resumedMediaExists(old *mediaDelivery) *mediaDelivery {
	for _, candidate := range w.runtime.Media {
		if candidate.ResumedFrom == old.Key {
			return candidate
		}
	}
	return nil
}

func (w *Worker) resumeMissingMedia(ctx context.Context, in companion.Input, old *mediaDelivery) (*mediaDelivery, string) {
	n, ok := w.sender.(resumeMissingMediaNative)
	if !ok {
		return nil, "native_resume_missing_not_supported"
	}
	if !old.UserCancelled || !resumableVoiceChunks(old) {
		return nil, "media_resume_missing_requires_verified_terminal_chunks"
	}
	// The original send authorization is not a perpetual capability.  Recheck
	// the route, task, permissions and immutable source binding at the point a
	// new native job would be created.
	if !w.taskActive(in) || !w.mediaAllowed(old) {
		return nil, "media_resume_missing_not_authorized"
	}
	if err := w.validateMediaEvidence(ctx, old); err != nil {
		return nil, "media_resume_missing_source_invalid"
	}
	if existing := w.resumedMediaExists(old); existing != nil {
		return existing, ""
	}
	if w.beforeSubmit(ctx) != nil {
		return nil, "media_resume_connection_paused"
	}
	key := "media-resume-" + messageHash(old.Key + "|" + in.TraceID)[:48]
	resumeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	status, err := n.ResumeMissingMediaJob(resumeCtx, old.ID, key)
	cancel()
	if err != nil {
		return nil, "native_resume_missing_failed"
	}
	if status.ID == "" || status.ThreadID != old.Input.Thread || status.SourceID != "" && status.SourceID != old.SourceID {
		return nil, "native_resume_missing_identity_mismatch"
	}
	next := *old
	next.Key, next.ResumedFrom, next.Trace = key, old.Key, in.TraceID
	next.Input = old.Input
	next.Input.TraceID, next.Input.OriginTraceID = in.TraceID, old.Trace
	next.ID, next.Status, next.Phase = status.ID, status.WithVoiceReceipts(), status.Phase
	next.State, next.Reason, next.UserCancelled = status.State, "media_resume_missing_requested", false
	next.CancelTrace, next.CancelSender, next.CancelledAt, next.StateVersion = "", "", time.Time{}, 0
	next.Created, next.CompletedAt, next.NotBefore, next.Next = time.Now().UTC(), time.Time{}, time.Time{}, time.Now()
	next.Retries, next.Checks, next.Signature = 0, 0, ""
	w.runtime.Media = append(w.runtime.Media, &next)
	w.mediaRecord(&next, "media_resume_missing_queued", "decision")
	w.refreshMediaParent(next.Trace)
	return &next, ""
}

func (w *Worker) controlQueue(ctx context.Context, in companion.Input, owner bool, raw json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var args struct {
		Action string
		Trace  string `json:"trace_id"`
	}
	if json.Unmarshal(raw, &args) != nil {
		return "", errors.New("invalid_queue_action")
	}
	switch args.Action {
	case "list", "pause", "resume", "cancel":
	default:
		return "", errors.New("invalid_queue_action")
	}
	n, ok := w.sender.(mediaNative)
	if !ok || w.runtime == nil {
		return "", errors.New("media_queue_unavailable")
	}
	if in.Sender == "" || in.Account != "" && in.Account != w.runtime.Account {
		return "", errors.New("queue_scope_not_authorized")
	}
	rows := []map[string]any{}
	blockedResume := map[string]bool{}
	for _, d := range w.runtime.Media {
		if d.terminal() && args.Trace == "" {
			continue
		}
		if d.Trace == in.TraceID || !queueOwned(in, d.Input, owner) || args.Trace != "" && args.Trace != d.Trace {
			continue
		}
		resumeError := ""
		var resumed *mediaDelivery
		if args.Action == "resume" && d.UserCancelled {
			if args.Trace == "" {
				resumeError = "media_cancelled_requires_explicit_resume_operation"
			} else {
				resumed, resumeError = w.resumeMissingMedia(ctx, in, d)
			}
		} else if args.Action != "list" && !d.terminal() {
			switch args.Action {
			case "pause":
				d.Paused = true
				w.persistErr = w.saveRuntime()
				if w.persistErr != nil {
					return "", w.persistErr
				}
				if d.ID != "" {
					w.cancelMedia(n, d, false, "media_paused_for_conversation")
					if d.State == "stopped" {
						d.State = "waiting"
					}
				}
			case "resume":
				if d.ID != "" {
					status, err := n.ToolSendStatus(ctx, d.ID)
					if err != nil {
						resumeError = "native_pause_status_unavailable"
					} else if status.ThreadID != d.Input.Thread || status.SourceID != "" && status.SourceID != d.SourceID {
						resumeError = "media_identity_mismatch"
					} else {
						d.Status = status
						if (status.State == "cancelled" || status.State == "failed") && !status.Retryable {
							resumeError = "native_cancelled_job_not_retryable"
						} else if status.CancelRequested && status.State != "cancelled" && status.State != "failed" && status.State != "accepted" {
							resumeError = "native_pause_not_settled"
						}
					}
				}
				if resumeError != "" {
					blockedResume[d.Trace] = true
					d.Paused = true
					d.Reason = resumeError
					break
				}
				d.Paused = false
				d.Next = time.Now()
				if d.ID == "" {
					d.State = "prepared"
				} else if d.State == "stopped" {
					d.State = "waiting"
				}
			case "cancel":
				d.Paused = false
				w.cancelMedia(n, d, true, "media_cancelled_by_agent_decision")
			}
		}
		acceptedParts, totalParts := 0, len(d.Status.VoiceChunks)
		for _, chunk := range d.Status.VoiceChunks {
			if chunk.State == "accepted" && chunk.HasReceipt {
				acceptedParts++
			}
		}
		row := map[string]any{"trace_id": d.Trace, "job_id": d.ID, "kind": d.Kind, "title": d.Title, "state": d.State, "paused": d.Paused, "cancelled": d.UserCancelled, "accepted_parts": acceptedParts, "total_parts": totalParts, "resume_error": resumeError}
		if resumed != nil {
			row["resumed_trace_id"], row["resumed_job_id"] = resumed.Trace, resumed.ID
		}
		rows = append(rows, row)
	}
	for _, p := range w.runtime.Plans {
		if !p.terminal() && p.MediaCaption && p.Trace != in.TraceID && queueOwned(in, p.Input, owner) && (args.Trace == "" || args.Trace == p.Trace) {
			switch args.Action {
			case "pause":
				p.Paused = true
			case "resume":
				p.Paused = blockedResume[p.Trace]
			case "cancel":
				p.Paused = false
				p.State = "stopped"
				p.StopReason = "media_queue_cancelled"
			}
			rows = append(rows, map[string]any{"trace_id": p.Trace, "kind": "caption", "state": p.State, "paused": p.Paused})
		}
	}
	for _, r := range w.runtime.MediaReplies {
		if r.terminal() || r.Trace == in.TraceID || !queueOwned(in, r.Input, owner) || args.Trace != "" && args.Trace != r.Trace {
			continue
		}
		switch args.Action {
		case "pause":
			r.Paused = true
		case "resume":
			r.Paused = blockedResume[r.Trace]
		case "cancel":
			r.State = "stopped"
			r.Paused = false
		}
		rows = append(rows, map[string]any{"trace_id": r.Trace, "kind": "pending_caption", "state": r.State, "paused": r.Paused})
	}
	if args.Action != "list" && len(rows) > 0 {
		w.persistErr = w.saveRuntime()
		if w.persistErr != nil {
			return "", w.persistErr
		}
	}
	b, _ := json.Marshal(map[string]any{"action": args.Action, "thread_id": in.Thread, "items": rows, "instruction": "已发内容保留真实回执。暂停保留原队列；已取消的分段语音只有在明确 resume 且每段状态已核实后，才会新建仅含未发段的任务，绝不重放已接受段。"})
	return string(b), nil
}

func (w *Worker) pauseInterruptedMedia(m linuxnative.Message) {
	if m.Direction == "outgoing" || m.Type != 1 && !m.IsQuotedText() {
		return
	}
	in := companion.Input{Thread: m.ThreadID, Sender: m.SenderID, TraceID: m.ID}
	// Pause only the sender's previous work and autonomous shares. The model
	// can explicitly manage other queues after server-verified owner discovery.
	_, _ = w.controlQueue(context.Background(), in, false, json.RawMessage(`{"action":"pause"}`))
}
