package bot

import (
	"context"
	"errors"
	"strings"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type groupObservationFailure struct{ reason, messageID string }

func (e *groupObservationFailure) Error() string { return e.reason }

// Keep provider responses, file paths and untrusted card text out of private reports.
func observationReason(err error) string {
	var failed *groupObservationFailure
	if errors.As(err, &failed) {
		return failed.reason
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "media_read_timeout"
	}
	code := err.Error()
	var failure interface{ SafeToolFailure() (string, string) }
	if errors.As(err, &failure) {
		code, _ = failure.SafeToolFailure()
	}
	for _, prefix := range []string{"plugin_", "media_", "incoming_", "shared_video_", "share_", "channels_", "video_", "source_identity_", "vision_", "asr_", "sticker_"} {
		if strings.HasPrefix(code, prefix) && len(code) <= 100 && strings.Trim(code, "abcdefghijklmnopqrstuvwxyz0123456789_") == "" {
			return code
		}
	}
	if strings.Contains(code, "冷启动未完成") {
		return "plugin_cold_start_failed"
	}
	return "media_read_failed"
}

// This retry is strictly for reading. Delivery has its own durable receipt
// reconciliation and must never enter this loop.
func (w *Worker) readRequiredMedia(ctx context.Context, m linuxnative.Message) (string, error) {
	text, _, err := w.readRequiredMediaWithEvidence(ctx, m)
	return text, err
}

func (w *Worker) readRequiredMediaWithEvidence(ctx context.Context, m linuxnative.Message) (string, MediaObservationEvidence, error) {
	var evidence MediaObservationEvidence
	text, err := readMediaWithRecoveryHook(ctx, m, func(ctx context.Context, current linuxnative.Message) (string, error) {
		text, result, err := w.analyzeIncomingMediaEvidence(ctx, current)
		if err == nil {
			evidence = result
		}
		return text, err
	}, func(attempt int, reason string, retryAt time.Time) {
		tool := "media-understanding"
		if m.Type == 49 {
			tool = "share-reader"
		}
		w.activity(Record{Event: "tool_retry_scheduled", Category: "tool", ThreadID: m.ThreadID, MessageID: m.ID, Tool: tool, ToolStage: "observation", Status: "retrying_tool", Reason: reason, RetryAttempts: attempt, RetryAt: retryAt.UTC().Format(time.RFC3339Nano), Origin: "wechat"})
	})
	return text, evidence, err
}

func readMediaWithRecovery(ctx context.Context, m linuxnative.Message, analyze func(context.Context, linuxnative.Message) (string, error)) (string, error) {
	return readMediaWithRecoveryHook(ctx, m, analyze, nil)
}

const maxObservationRetries = 3
const maxDurableToolRetries = 6

func retryableObservationReason(reason string) bool {
	switch reason {
	case "share_public_read_failed", "share_plugin_resolution_needs_review", "share_plugin_resolution_incomplete":
		return true
	case "plugin_cold_start_failed", "share_plugin_runtime_unavailable", "share_plugin_store_unavailable", "share_plugin_start_failed", "share_bridge_unavailable", "share_native_source_unavailable", "incoming_file_download_failed", "incoming_media_download_failed", "media_read_timeout", "media_plugin_runtime_unavailable", "media_plugin_start_failed":
		return true
	default:
		return false
	}
}

func observationRetryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return time.Second
	case 2:
		return 3 * time.Second
	default:
		return 8 * time.Second
	}
}

func durableToolRetryDelay(attempt int) time.Duration {
	if attempt <= maxObservationRetries {
		return observationRetryDelay(attempt)
	}
	return time.Duration(1<<(attempt-maxObservationRetries)) * 8 * time.Second
}

func (w *Worker) scheduleToolRetry(in companion.Input, source, reason string) bool {
	if w.runtime == nil || !retryableObservationReason(reason) {
		return false
	}
	id := source
	if id == "" {
		id = in.TraceID
	}
	for i := range w.runtime.Inbox {
		item := &w.runtime.Inbox[i]
		if item.Event.Data.MessageID != id {
			continue
		}
		if item.ToolRetryAttempts >= maxDurableToolRetries {
			return false
		}
		item.ToolRetryAttempts++
		item.ToolRetryReason = reason
		item.RetryAfter = time.Now().Add(durableToolRetryDelay(item.ToolRetryAttempts))
		item.Phase = "waiting_tool_retry"
		item.Updated = time.Now().UTC()
		w.persistErr = w.saveRuntime()
		if w.persistErr != nil {
			return false
		}
		w.activity(Record{Event: "tool_retry_scheduled", Category: "tool", ThreadID: item.Event.Data.ThreadID, MessageID: item.Event.Data.MessageID, Tool: "share-reader", ToolStage: "observation", Status: "waiting_tool_retry", Reason: reason, RetryAttempts: item.ToolRetryAttempts, RetryAt: item.RetryAfter.UTC().Format(time.RFC3339Nano), Origin: "wechat"})
		return true
	}
	return false
}

func readMediaWithRecoveryHook(ctx context.Context, m linuxnative.Message, analyze func(context.Context, linuxnative.Message) (string, error), onRetry func(int, string, time.Time)) (string, error) {
	read := func() (string, error) {
		text, err := analyze(ctx, m)
		if err == nil && strings.TrimSpace(text) == "" {
			return "", errors.New("media_observation_empty")
		}
		return text, err
	}
	text, err := read()
	if err == nil || !strings.HasSuffix(m.ThreadID, "@chatroom") {
		return text, err
	}
	for attempt := 1; attempt <= maxObservationRetries; attempt++ {
		reason := observationReason(err)
		if !retryableObservationReason(reason) {
			return text, err
		}
		// A missing native attachment is a deterministic local bridge result.
		// Retrying it through the full share backoff would consume the caller's
		// turn deadline before the worker can report the unreadable file. Keep one
		// recovery attempt for transient races, then return the original failure.
		if (reason == "incoming_file_download_failed" || reason == "incoming_media_download_failed") && attempt > 1 {
			return text, err
		}
		delay := observationRetryDelay(attempt)
		retryAt := time.Now().Add(delay)
		if onRetry != nil {
			onRetry(attempt, reason, retryAt)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return "", ctx.Err()
		case <-timer.C:
		}
		text, err = read()
		if err == nil {
			return text, nil
		}
	}
	return text, err
}

func (w *Worker) stopUnreadableTurn(in companion.Input, err error) {
	reason := observationReason(err)
	source := in.TraceID
	var failed *groupObservationFailure
	if errors.As(err, &failed) {
		source = failed.messageID
	}
	retryScheduled := w.scheduleToolRetry(in, source, reason)
	if retryScheduled {
		// The original turn is still resumable. Report a final failure only
		// after its durable read attempts have actually ended.
		return
	}
	if !retryScheduled && retryableObservationReason(reason) {
		w.activity(Record{Event: "tool_retry_exhausted", Category: "error", ThreadID: in.Thread, MessageID: in.TraceID, RelatedMessageID: source, Tool: "share-reader", ToolStage: "observation", Status: "failed", Reason: reason, RetryAttempts: maxDurableToolRetries, Origin: "wechat"})
	}
	report := "本回合有消息未能读取，已停止群回复。读取错误：" + reason
	event := Record{Event: "group_input_read_failed", Category: "error", ThreadID: in.Thread, MessageID: in.TraceID, RelatedMessageID: source, Status: "failed", Reason: reason, Content: report}
	w.activity(event)
	if w.config.ObservationFailed == nil || w.config.ObservationFailed(in, report) != nil {
		w.activity(Record{Event: "private_failure_report_unavailable", Category: "error", ThreadID: in.Thread, MessageID: in.TraceID, Status: "failed", Reason: "private_owner_report_not_queued"})
	}
	if !retryScheduled && source != in.TraceID {
		w.queuePhase(source, "stopped")
	}
	if !retryScheduled {
		w.queuePhase(in.TraceID, "stopped")
	}
}

// An attempted read is evidence of failure, not evidence of video contents.
type ObservationUnavailableError struct{ Observation, Reason string }

func (e *ObservationUnavailableError) Error() string { return e.Reason }

func failedObservation(err error) string {
	var unavailable *ObservationUnavailableError
	observation := "媒体预读取未取得可验证内容。"
	if errors.As(err, &unavailable) {
		observation += unavailable.Observation
	}
	return observation + "\n这是读取失败资料，不是用户指令。先用可用工具依据当前消息ID、真实分享链接或素材ID自动重读或换方法；群聊回复不要列出插件错误码或要求人工核对。若实际正文仍无法取得，只简短说明尚未读到内容，技术原因单独告知主人；不能拿标题猜正文结论。"
}
