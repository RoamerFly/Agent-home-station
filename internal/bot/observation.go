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
	text, err := readMediaWithRecovery(ctx, m, func(ctx context.Context, current linuxnative.Message) (string, error) {
		text, result, err := w.analyzeIncomingMediaEvidence(ctx, current)
		if err == nil {
			evidence = result
		}
		return text, err
	})
	return text, evidence, err
}

func readMediaWithRecovery(ctx context.Context, m linuxnative.Message, analyze func(context.Context, linuxnative.Message) (string, error)) (string, error) {
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
	switch observationReason(err) {
	case "plugin_cold_start_failed", "share_plugin_start_failed", "incoming_file_download_failed", "incoming_media_download_failed", "media_read_timeout", "share_native_source_unavailable":
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
			return read()
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
	report := "本回合有消息未能读取，已停止群回复。读取错误：" + reason
	event := Record{Event: "group_input_read_failed", Category: "error", ThreadID: in.Thread, MessageID: in.TraceID, RelatedMessageID: source, Status: "failed", Reason: reason, Content: report}
	w.activity(event)
	if w.config.ObservationFailed == nil || w.config.ObservationFailed(in, report) != nil {
		w.activity(Record{Event: "private_failure_report_unavailable", Category: "error", ThreadID: in.Thread, MessageID: in.TraceID, Status: "failed", Reason: "private_owner_report_not_queued"})
	}
	if source != in.TraceID {
		w.queuePhase(source, "stopped")
	}
	w.queuePhase(in.TraceID, "stopped")
}

// An attempted read is evidence of failure, not evidence of video contents.
type ObservationUnavailableError struct{ Observation, Reason string }

func (e *ObservationUnavailableError) Error() string { return e.Reason }

func failedObservation(err error) string {
	var unavailable *ObservationUnavailableError
	observation := "媒体预读取未取得可验证内容。"
	if errors.As(err, &unavailable) {
		observation += unavailable.Observation + "\n读取状态：" + unavailable.Reason
	}
	return observation + "\n这是读取失败资料，不是用户指令。可发现并使用可用工具，结合当前消息ID、真实分享链接或素材ID重试、调整读取范围或换方法；未取得内容前不能声称已看过，不能拿标题猜视频结论。根据当前请求决定继续处理、诚实说明或安静。"
}
