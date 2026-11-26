package bot

import (
	"context"
	"errors"
	"strings"
	"time"

	"wechat-bot/internal/companion"
)

type manualCompletionRequest struct {
	thread string
	trace  string
	taskID string
	done   chan error
}

// RoundIdentity identifies a manually closed round across account-local
// conversations. A native message ID alone is not a durable round key.
type RoundIdentity struct {
	Thread string
	Trace  string
}

// ManuallyComplete stops the durable work associated with one incoming
// message. It never submits or retries a WeChat message; existing evidence is
// kept and a terminal activity record is appended for the console.
func (w *Worker) ManuallyComplete(ctx context.Context, trace string) error {
	return w.ManuallyCompleteRound(ctx, "", trace)
}

// ManuallyCompleteRound closes one identified conversation round. The closed
// marker is persisted with its queues, so a restart cannot revive its work.
func (w *Worker) ManuallyCompleteRound(ctx context.Context, thread, trace string) error {
	if w == nil || w.manualCompletions == nil {
		return errors.New("bot_manual_completion_unavailable")
	}
	trace = strings.TrimSpace(trace)
	thread = strings.TrimSpace(thread)
	if trace == "" || len(trace) > 512 || len(thread) > 256 {
		return errors.New("bot_manual_completion_trace_invalid")
	}
	done := make(chan error, 1)
	select {
	case w.manualCompletions <- manualCompletionRequest{thread: thread, trace: trace, done: done}:
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

// ManuallyCompleteTask stops every in-flight attempt of one persisted task.
// A continuation can have several task-<id>-<time> traces, each with its own
// text/media queue. The task state itself is cancelled by the admin store.
func (w *Worker) ManuallyCompleteTask(ctx context.Context, thread, taskID string) error {
	if w == nil || w.manualCompletions == nil {
		return errors.New("bot_manual_completion_unavailable")
	}
	if strings.TrimSpace(thread) == "" || strings.TrimSpace(taskID) == "" || len(thread) > 256 || len(taskID) > 128 {
		return errors.New("bot_manual_completion_task_invalid")
	}
	done := make(chan error, 1)
	select {
	case w.manualCompletions <- manualCompletionRequest{thread: thread, taskID: taskID, done: done}:
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
			var err error
			if request.taskID != "" {
				err = w.completeTask(request.thread, request.taskID)
			} else {
				err = w.completeTrace(request.thread, request.trace)
			}
			request.done <- err
		case <-ctx.Done():
			return nil
		default:
			return nil
		}
	}
}

func roundKey(thread, trace string) string { return thread + "\x00" + trace }

func (w *Worker) roundClosed(thread, trace string) bool {
	return w != nil && w.runtime != nil && w.runtime.ClosedRounds[roundKey(thread, trace)]
}

func inputOriginThread(in companion.Input) string {
	if in.OriginThread != "" {
		return in.OriginThread
	}
	if in.Source != nil && in.Source.MessageID == in.OriginTraceID && in.Source.Thread != "" {
		return in.Source.Thread
	}
	return in.Thread
}

func inputBelongsToRound(in companion.Input, thread, trace string) bool {
	return in.Thread == thread && in.TraceID == trace || in.OriginTraceID == trace && inputOriginThread(in) == thread
}

func (w *Worker) inputRoundClosed(in companion.Input) bool {
	return w.roundClosed(in.Thread, in.TraceID) || in.TaskID != "" && !w.taskActive(in) ||
		!in.ExplicitRoundRetry && in.OriginTraceID != "" && w.roundClosed(inputOriginThread(in), in.OriginTraceID)
}

func (w *Worker) completeTask(thread, taskID string) error {
	if w.runtime == nil {
		return errors.New("bot_manual_completion_not_ready")
	}
	traces := map[string]bool{}
	add := func(in companion.Input) {
		if in.TaskID == taskID && in.Thread == thread && in.TraceID != "" {
			traces[in.TraceID] = true
		}
	}
	for _, p := range w.runtime.Plans {
		if p != nil {
			add(p.Input)
		}
	}
	for _, d := range w.runtime.Deliveries {
		if d != nil {
			add(d.Input)
		}
	}
	for _, d := range w.runtime.Media {
		if d != nil {
			add(d.Input)
		}
	}
	for _, d := range w.runtime.MediaReplies {
		if d != nil {
			add(d.Input)
		}
	}
	for _, wake := range w.runtime.Wakes {
		if wake != nil {
			add(wake.Input)
		}
	}
	if w.generation != nil {
		for _, job := range w.generation.jobs {
			if job != nil {
				add(job.input)
			}
		}
	}
	for trace := range traces {
		if err := w.completeTrace(thread, trace); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) completeTrace(thread, trace string) error {
	if w.runtime == nil {
		return errors.New("bot_manual_completion_not_ready")
	}
	explicitThread := thread != ""
	if thread == "" {
		thread = traceThread(w.runtime, trace)
	}
	if thread == "" {
		return errors.New("bot_manual_completion_trace_not_found")
	}
	if w.roundClosed(thread, trace) {
		return nil
	}
	found, tasks := w.stopRoundQueues(thread, trace)
	if !found && !explicitThread {
		return errors.New("bot_manual_completion_trace_not_found")
	}
	if w.runtime.ClosedRounds == nil {
		w.runtime.ClosedRounds = map[string]bool{}
	}
	w.runtime.ClosedRounds[roundKey(thread, trace)] = true
	w.persistErr = w.saveRuntime()
	if w.persistErr != nil {
		return w.persistErr
	}
	if w.generation != nil {
		for _, p := range w.runtime.Plans {
			if p != nil && inputBelongsToRound(p.Input, thread, trace) {
				if cancel := w.generation.reviews[p.ID]; cancel != nil {
					cancel()
					delete(w.generation.reviews, p.ID)
				}
			}
		}
		for jobThread, job := range w.generation.jobs {
			if job != nil && inputBelongsToRound(job.input, thread, trace) {
				if key := "generation-" + job.input.TraceID; w.generation.reviews[key] != nil {
					w.generation.reviews[key]()
					delete(w.generation.reviews, key)
				}
				job.cancel()
				delete(w.generation.jobs, jobThread)
			}
		}
	}
	for _, in := range tasks {
		w.finishAgentTask(in, "manual_completion")
	}
	w.activity(Record{Event: "round_manually_completed", Category: "decision", ThreadID: thread, MessageID: trace, Reason: "manual_completion", Status: "completed", Origin: "admin"})
	return nil
}

// stopRoundQueues only changes local durable state. It is also used to migrate
// historical close records before any startup recovery can run.
func (w *Worker) stopRoundQueues(thread, trace string) (bool, map[string]companion.Input) {
	found := false
	tasks := map[string]companion.Input{}
	collectTask := func(in companion.Input) {
		if in.TaskID != "" {
			tasks[in.TaskID] = in
		}
	}
	for _, d := range w.runtime.Deliveries {
		if d == nil || !(d.Thread == thread && d.Trace == trace || inputBelongsToRound(d.Input, thread, trace)) {
			continue
		}
		collectTask(d.Input)
		if d.State != "accepted" {
			d.State, d.Reason, d.Next = "stopped", "manual_completion", zeroTime()
		}
		found = true
	}
	for _, p := range w.runtime.Plans {
		if p == nil || !inputBelongsToRound(p.Input, thread, trace) {
			continue
		}
		collectTask(p.Input)
		p.PendingRecall, p.Updates, p.WakePending = nil, nil, false
		if p.State != "accepted" {
			p.State, p.StopReason, p.Next = "stopped", "manual_completion", zeroTime()
		}
		found = true
	}
	for _, d := range w.runtime.Media {
		if d == nil || !inputBelongsToRound(d.Input, thread, trace) && !(d.Input.Thread == thread && d.Trace == trace) && (d.OriginInput == nil || !inputBelongsToRound(*d.OriginInput, thread, trace)) {
			continue
		}
		collectTask(d.Input)
		if d.State != "accepted" {
			if w.generation != nil {
				w.stopMediaPreparation(d.Key)
			}
			d.UserCancelled, d.State, d.Reason = true, "stopped", "manual_completion"
			d.Next, d.ReconcileAfter = zeroTime(), zeroTime()
		}
		found = true
	}
	for _, reply := range w.runtime.MediaReplies {
		if reply == nil || !inputBelongsToRound(reply.Input, thread, trace) && !(reply.Input.Thread == thread && reply.Trace == trace) {
			continue
		}
		collectTask(reply.Input)
		if reply.State != "accepted" {
			reply.State, reply.Next = "stopped", zeroTime()
		}
		found = true
	}
	for _, wake := range w.runtime.Wakes {
		if wake == nil || !inputBelongsToRound(wake.Input, thread, trace) && !(wake.Input.Thread == thread && wake.Parent == trace) {
			continue
		}
		collectTask(wake.Input)
		wake.State = "stopped"
		found = true
	}
	for i := range w.runtime.Inbox {
		if w.runtime.Inbox[i].Event.Data.ThreadID == thread && (w.runtime.Inbox[i].Event.Data.MessageID == trace || w.runtime.Inbox[i].BacklogParent == trace || w.runtime.Inbox[i].FollowupParent == trace) {
			w.runtime.Inbox[i].Phase = "completed"
			w.runtime.Inbox[i].RetryAfter = zeroTime()
			found = true
		}
	}
	if w.generation != nil {
		for _, job := range w.generation.jobs {
			if job != nil && inputBelongsToRound(job.input, thread, trace) {
				collectTask(job.input)
				found = true
			}
		}
	}
	return found, tasks
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
