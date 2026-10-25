package bot

import (
	"context"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

func modelRecord(thread, message string, d deepseek.Diagnostics) Record {
	return Record{ThreadID: thread, MessageID: message, ModelHTTPStatus: d.HTTPStatus, ModelAttempts: d.Attempts, ModelMaxAttempts: d.MaxAttempts, ModelStage: d.Stage, ModelRetryDelayMS: d.RetryDelayMS, DurationMS: d.DurationMS, Origin: "model"}
}

func (w *Worker) modelContext(ctx context.Context, thread, message string) context.Context {
	ctx = companion.WithDecisionObserver(ctx, func(d companion.DecisionDiagnostic) {
		event, status := "model_decision_repair", "retrying"
		if d.Code == "decision_format_recovered" {
			event, status = "model_decision_recovered", "recovered"
		}
		w.activity(Record{ThreadID: thread, MessageID: message, Event: event, Category: "system", Reason: d.Code, Status: status, ModelAttempts: d.Attempt, ModelMaxAttempts: 2, ModelStage: "decision_format", Origin: "model"})
	})
	return deepseek.WithRequestObserver(ctx, func(event string, d deepseek.Diagnostics) {
		record := modelRecord(thread, message, d)
		record.Event, record.Category, record.Reason, record.Status = event, "system", d.Code, "retrying"
		if event == "model_recovered" {
			record.Status = "recovered"
		}
		w.activity(record)
	})
}

func (w *Worker) modelFailed(thread, message string, err error) {
	record := Record{ThreadID: thread, MessageID: message}
	if d, ok := deepseek.ErrorDiagnostics(err); ok {
		record = modelRecord(thread, message, d)
	}
	record.Event, record.Category, record.Reason, record.Status = "model_failed", "error", err.Error(), "failed"
	w.activity(record)
}
