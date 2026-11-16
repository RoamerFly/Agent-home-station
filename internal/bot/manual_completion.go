package bot

import (
	"context"
	"errors"
	"strings"
	"time"
)

type manualCompletionRequest struct {
	trace string
	done  chan error
}

// ManuallyComplete stops the durable work associated with one incoming
// message. It never submits or retries a WeChat message; existing evidence is
// kept and a terminal activity record is appended for the console.
func (w *Worker) ManuallyComplete(ctx context.Context, trace string) error {
	if w == nil || w.manualCompletions == nil {
		return errors.New("bot_manual_completion_unavailable")
	}
	trace = strings.TrimSpace(trace)
	if trace == "" {
		return errors.New("bot_manual_completion_trace_invalid")
	}
	done := make(chan error, 1)
	select {
	case w.manualCompletions <- manualCompletionRequest{trace: trace, done: done}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Worker) drainManualCompletions(ctx context.Context) error {
	for {
		select {
		case request := <-w.manualCompletions:
			err := w.completeTrace(request.trace)
			request.done <- err
		case <-ctx.Done():
			return nil
		default:
			return nil
		}
	}
}

func (w *Worker) completeTrace(trace string) error {
	if w.runtime == nil {
		return errors.New("bot_manual_completion_not_ready")
	}
	found := false
	for _, d := range w.runtime.Deliveries {
		if d == nil || d.Trace != trace || d.State == "accepted" || d.State == "stopped" {
			continue
		}
		d.State = "stopped"
		d.Reason = "manual_completion"
		d.Next = zeroTime()
		found = true
	}
	for _, p := range w.runtime.Plans {
		if p == nil || p.Input.TraceID != trace || p.terminal() {
			continue
		}
		p.State = "stopped"
		p.StopReason = "manual_completion"
		p.Next = zeroTime()
		found = true
	}
	for i := range w.runtime.Inbox {
		if w.runtime.Inbox[i].Event.Data.MessageID == trace && w.runtime.Inbox[i].Phase != "completed" && w.runtime.Inbox[i].Phase != "replied" {
			w.runtime.Inbox[i].Phase = "completed"
			found = true
		}
	}
	if !found {
		return errors.New("bot_manual_completion_trace_not_found")
	}
	w.persistErr = w.saveRuntime()
	if w.persistErr != nil {
		return w.persistErr
	}
	w.activity(Record{Event: "round_manually_completed", Category: "decision", ThreadID: traceThread(w.runtime, trace), MessageID: trace, Reason: "manual_completion", Status: "completed", Origin: "admin"})
	return nil
}

func traceThread(runtime *runtimeState, trace string) string {
	for _, item := range runtime.Inbox {
		if item.Event.Data.MessageID == trace {
			return item.Event.Data.ThreadID
		}
	}
	for _, d := range runtime.Deliveries {
		if d != nil && d.Trace == trace {
			return d.Thread
		}
	}
	return ""
}

// Kept as a named helper so completion clears retry scheduling without
// changing the meaning of an omitted timestamp in persisted JSON.
func zeroTime() time.Time { return time.Time{} }
