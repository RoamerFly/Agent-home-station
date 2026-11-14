package bot

import (
	"context"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

func modelRecord(thread, message string, d deepseek.Diagnostics) Record {
	return Record{ThreadID: thread, MessageID: message, ModelHTTPStatus: d.HTTPStatus, ModelAttempts: d.Attempts, ModelMaxAttempts: d.MaxAttempts, ModelStage: d.Stage, ModelRetryDelayMS: d.RetryDelayMS, ModelFinishReason: d.FinishReason, ModelContentBytes: d.ContentBytes, ModelJSONValid: d.JSONValid, ModelFormatAttempt: d.FormatAttempt, ModelMaxFormatAttempts: d.MaxFormatAttempts, ModelProtocolCode: d.ProtocolCode, DurationMS: d.DurationMS, Origin: "model"}
}

func modelEventRecord(thread, message, event string, d deepseek.Diagnostics) Record {
	record := modelRecord(thread, message, d)
	record.Event, record.Category, record.Reason, record.Status = event, "system", d.Code, "received"
	if d.Code != "" {
		record.Category, record.Status = "error", "failed"
	}
	if event == "model_retry" || event == "model_grounding_format_repair" {
		record.Status = "retrying"
	}
	if event == "model_recovered" || event == "model_grounding_format_recovered" {
		record.Status = "recovered"
	}
	return record
}

func (w *Worker) modelContext(ctx context.Context, thread, message string) context.Context {
	ctx = companion.WithToolObserver(ctx, func(d companion.ToolDiagnostic) {
		w.activity(toolDiagnosticRecord(thread, message, d))
	})
	ctx = companion.WithDecisionObserver(ctx, func(d companion.DecisionDiagnostic) {
		event, status := "model_decision_repair", "retrying"
		if d.Code == "decision_format_recovered" {
			event, status = "model_decision_recovered", "recovered"
		}
		w.activity(Record{ThreadID: thread, MessageID: message, Event: event, Category: "system", Reason: d.Code, Status: status, ModelAttempts: d.Attempt, ModelMaxAttempts: 2, ModelStage: "decision_format", Origin: "model"})
	})
	return deepseek.WithRequestObserver(ctx, func(event string, d deepseek.Diagnostics) {
		w.activity(modelEventRecord(thread, message, event, d))
	})
}

func toolDiagnosticRecord(thread, message string, d companion.ToolDiagnostic) Record {
	category := "tool"
	if d.Event == "tool_failed" {
		category = "error"
	}
	return Record{ThreadID: thread, MessageID: message, Event: d.Event, Category: category, Status: d.Status,
		CallID: d.CallID, Tool: d.Tool, ToolAttempt: d.Attempt, ToolRetries: d.Retries,
		ToolRequests: d.Requests, ToolHTTPStatus: d.HTTPStatus, ToolStage: d.Stage,
		ToolCode: d.Code, DurationMS: d.DurationMS, Origin: "tool"}
}

func (w *Worker) modelFailed(thread, message string, err error) {
	record := Record{ThreadID: thread, MessageID: message}
	if review, ok := companion.ReviewFailureFromError(err); ok {
		record.ReviewFailure, record.Reason = &review, review.Code
		if review.Request != nil {
			record = modelRecord(thread, message, *review.Request)
			record.ReviewFailure, record.Reason = &review, review.Code
		}
	}
	if d, ok := deepseek.ErrorDiagnostics(err); ok && record.ReviewFailure == nil {
		record = modelRecord(thread, message, d)
	}
	if record.Reason == "" {
		record.Reason = err.Error()
	}
	record.Event, record.Category, record.Status = "model_failed", "error", "failed"
	w.activity(record)
}
