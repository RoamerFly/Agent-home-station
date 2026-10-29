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
	return mcptools.Tool{Name: "wechat_queue_control", Description: "Inspect/pause/resume/cancel retained attachment queues in THIS conversation. An addressed interruption temporarily pauses your prior media and proactive shares. Decide whether to resume, cancel remaining parts or keep paused; unrelated chat need not cancel. Never resubmit accepted segments. trace_id selects one original turn; omitted selects queues belonging to current sender plus autonomous shares. The real owner may manage all queues here. Cancel removes pending content, not already delivered messages; recall is separate. Input {action:list|pause|resume|cancel,trace_id?:string}.", InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["list","pause","resume","cancel"]},"trace_id":{"type":"string"}},"required":["action"],"additionalProperties":false}`)}
}

func queueOwned(in, old companion.Input, owner bool) bool {
	return in.Thread == old.Thread && (owner || old.Sender == in.Sender || old.Surf)
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
		if args.Action != "list" && !d.terminal() {
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
				if !d.UserCancelled {
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
				}
			case "cancel":
				d.Paused = false
				w.cancelMedia(n, d, true, "media_cancelled_by_agent_decision")
			}
		}
		rows = append(rows, map[string]any{"trace_id": d.Trace, "job_id": d.ID, "kind": d.Kind, "title": d.Title, "state": d.State, "paused": d.Paused, "cancelled": d.UserCancelled, "resume_error": resumeError})
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
	b, _ := json.Marshal(map[string]any{"action": args.Action, "thread_id": in.Thread, "items": rows, "instruction": "已发内容保留真实回执。暂停保留原队列，继续沿用原任务，取消只处理剩余内容；不要重新下载、生成或另建相同队列。"})
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
