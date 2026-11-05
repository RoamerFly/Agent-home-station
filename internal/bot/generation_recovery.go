package bot

import (
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

// modelContinuationDelay is intentionally narrower than the provider's
// in-request retry.  deepseek.Client already repeats one short transient
// request.  This layer persists only a remaining retryable provider failure;
// invalid credentials and exhausted balance are terminal until an operator
// changes configuration or account state.
func modelContinuationDelay(err error) (time.Duration, string, bool) {
	d, ok := deepseek.ErrorDiagnostics(err)
	if !ok || !d.Retryable {
		return 0, "", false
	}
	switch d.Code {
	case "model_authentication_failed", "model_insufficient_balance", "model_invalid_parameters", "model_endpoint_or_model_missing":
		return 0, "", false
	}
	if d.RetryDelayMS > 0 {
		return time.Duration(d.RetryDelayMS) * time.Millisecond, d.Code, true
	}
	return DeliveryRetryBackoff(1), d.Code, true
}

// scheduleModelContinuation keeps the original input and any already stored
// source evidence. It runs only before an effectful tool begins: replaying a
// model after a tool could replay an external action. Tool-owning plugins use
// their own durable checkpoint and recovery task instead.
func (w *Worker) scheduleModelContinuation(in companion.Input, err error, toolStarted bool) bool {
	delay, code, retryable := modelContinuationDelay(err)
	if !retryable || (toolStarted && !onlyReadOnlyCheckpoints(in.RecoveryToolResults)) || !w.taskActive(in) {
		return false
	}
	if in.TaskID != "" && w.config.AgentTaskRetry != nil {
		// Record the failed provider attempt before putting the durable task back
		// into its waiting state.  activity(model_failed) intentionally projects
		// a failed queue state for ordinary runs, so doing this afterwards would
		// overwrite retrying_model and lose the provider supplied Retry-After.
		w.modelFailed(in.Thread, in.TraceID, err)
		w.config.AgentTaskRetry(in, delay, code)
		w.activity(Record{Event: "model_retry_scheduled", Category: "decision", ThreadID: in.Thread, MessageID: in.TraceID, Status: "retrying_model", Reason: code, RetryAt: time.Now().Add(delay).Format(time.RFC3339)})
		return true
	}
	if w.runtime == nil {
		return false
	}
	for i := range w.runtime.Inbox {
		item := &w.runtime.Inbox[i]
		if item.Event.Data.MessageID != in.TraceID || item.ToolStarted {
			continue
		}
		// See the corresponding task path above: record the provider failure
		// first, then atomically publish the durable continuation checkpoint.
		w.modelFailed(in.Thread, in.TraceID, err)
		item.Phase = "retrying_model"
		item.RetryAfter = time.Now().Add(delay)
		item.Updated = time.Now().UTC()
		w.persistErr = w.saveRuntime()
		if w.persistErr != nil {
			return false
		}
		w.activity(Record{Event: "model_retry_scheduled", Category: "decision", ThreadID: in.Thread, MessageID: in.TraceID, Status: "retrying_model", Reason: code, RetryAt: item.RetryAfter.Format(time.RFC3339)})
		return true
	}
	return false
}
