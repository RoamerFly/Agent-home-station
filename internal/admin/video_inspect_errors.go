package admin

import (
	"context"
	"errors"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/mcptools"
)

type videoInspectionFailure struct {
	code, message string
	cause         error
}

func (e *videoInspectionFailure) Error() string                     { return e.code }
func (e *videoInspectionFailure) Unwrap() error                     { return e.cause }
func (e *videoInspectionFailure) SafeToolFailure() (string, string) { return e.code, e.message }
func videoInspectionError(err error) error {
	if err == nil {
		return nil
	}
	var safe interface{ SafeToolFailure() (string, string) }
	var web *mcptools.WebError
	if errors.As(err, &safe) || errors.As(err, &web) {
		return err
	}
	code, message := "video_inspection_failed", "视频读取未完成；请核对原件是否仍可取得、读取权限和已返回的续读起点，按已返回的读取范围继续处理"
	if d, ok := deepseek.ErrorDiagnostics(err); ok {
		code, message = d.Code, deepseek.UserMessage(err)+"；可按真实原因调整模型参数或稍后从未读帧续读"
	} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		code, message = "video_inspection_interrupted", "本次视频读取已超时或中止；已读范围保留，可从未读起点继续"
	} else {
		messages := map[string]string{
			"video_inspection_result_capacity":        "已读结果接近1MiB工具结果传输封套；保留已返回批次，从next_start_frame继续，或减小分析文字预算",
			"video_inspect_probe_failed":              "无法解析实际视频；请确认是已解密原件，且运行节点已安装ffmpeg和ffprobe",
			"video_inspect_metadata_invalid":          "原件的帧数或时长无效；需先取得可解析的视频原件",
			"video_inspect_decode_failed":             "实际视频帧解码失败；可核对原件格式、损坏情况与解码器后重试",
			"video_inspect_frame_evidence_incomplete": "解码帧数或时间戳不完整；未把旧图片当作新帧，可缩小页面后重试",
			"video_inspect_timestamp_invalid":         "解码器未返回有效帧时间戳；需核对视频时间基后重试",
			"video_audio_transcription_disabled":      "音轨转写未启用；当前可以继续读取画面",
			"video_audio_unavailable":                 "此时间范围没有可解码的音轨；可检查实际音轨结尾或选择其他时间范围",
			"video_audio_transcription_failed":        "实际音轨已尝试转写但提供商没有返回有效文本；核对语音服务配置或稍后重试",
			"video_source_not_authorized":             "来源会话未授权。可信管理员可在同一账号内用source_thread_id指定已授权群聊；普通成员只能读取当前会话",
			"video_source_authorization_revoked":      "读取期间来源会话的授权已变更，结果未返回；请重新核对当前权限",
			"video_audio_time_budget_exhausted":       "剩余执行时间不足以转写；保存检查点后从当前音轨起点继续",
		}
		if err.Error() == "vision image size limit" {
			return &videoInspectionFailure{"vision_payload_too_large", "本批图像字节超过视觉传输预算；可减小batch_size或max_dimension后从未读帧继续，页大小不必缩短", err}
		}
		if err.Error() == "video_audio_limit" {
			return &videoInspectionFailure{"audio_payload_too_large", "本段PCM超过音轨传输预算；减小audio_chunk_seconds后继续，所需总时间范围不受此默认值限制", err}
		}
		if text, ok := messages[err.Error()]; ok {
			code, message = err.Error(), text
		}
	}
	return &videoInspectionFailure{code, message, err}
}
