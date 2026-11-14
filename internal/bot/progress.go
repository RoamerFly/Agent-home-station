package bot

import (
	"context"
	"errors"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/sending"
)

func (w *Worker) toolProgress(character *companion.Engine, input companion.Input) companion.ProgressHandler {
	return func(ctx context.Context, text string) (bool, error) {
		w.queuePhase(input.TraceID, "tool_running")
		if w.persistErr != nil {
			return false, w.persistErr
		}
		if text == "" || w.config.Options == nil || !w.config.Options.AcknowledgeTools || !character.Config.Permissions.ReplyText || input.Surf || input.Mode == "proactive" {
			return false, nil
		}
		if err := character.Claim(input); err != nil {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(w.replyDelay(character)):
		}
		body := text
		sendCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		_, err := w.sendText(sendCtx, input, -1, body)
		cancel()
		// Progress is a separate delivery slot and never completes the incoming
		// task. Persist this before entering potentially effectful tool code.
		w.queuePhase(input.TraceID, "tool_running")
		if w.persistErr != nil {
			return false, w.persistErr
		}
		if err != nil {
			status := "failed"
			if errors.Is(err, sending.ErrOutcomeUnknown) {
				status = "unknown"
			}
			w.activity(Record{Event: "tool_progress_failed", Category: "error", ThreadID: input.Thread, MessageID: input.TraceID, Content: body, Reason: sending.FailureReason(err), Status: status, Origin: "bot"})
			return false, nil
		}
		if err = w.acceptTextMemory(character, input, -1, body); err != nil {
			return false, err
		}
		w.activity(Record{Event: "message_progress", Category: "sent", ThreadID: input.Thread, MessageID: input.TraceID, Content: body, Reason: "tool_progress_accepted", Status: "accepted", Origin: "bot"})
		return true, nil
	}
}
