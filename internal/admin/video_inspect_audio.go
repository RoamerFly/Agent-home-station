package admin

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"wechat-bot/internal/bailian"
	"wechat-bot/internal/mcptools"
)

// A requested audio range is processed in transport-sized chunks; the range
// itself is not clamped to a default chunk duration.
func (p *permittedTools) inspectAudioRange(ctx context.Context, data []byte, in videoInspectionInput, length, total float64, out map[string]any) error {
	requested := in.AudioChunk
	chunk := requested
	if chunk == 0 {
		chunk = 180
	}
	// 16kHz mono PCM16 plus WAV header must fit this in-memory ASR transport.
	byteSeconds := float64((8<<20)-1024) / 32000
	chunk = math.Min(chunk, byteSeconds)
	next, end := in.AudioStart, in.AudioStart+length
	parts := []map[string]any{}
	texts := []string{}
	resultBytes := 0
	early := false
	timeout := 60
	if in.AudioTimeout != nil {
		timeout = *in.AudioTimeout
	}
	if timeout < 0 || int64(timeout) > math.MaxInt64/int64(1e9) {
		return toolInputError("audio_timeout_seconds超出可表示范围")
	}
	a := mediaUnderstanding{manager: p.manager}
	for next < end {
		count := math.Min(chunk, end-next)
		text, seconds, err := a.videoAudioRangeTimed(ctx, data, next, count, timeout)
		noSpeech := bailian.IsNoSpeech(err)
		if err != nil && !noSpeech {
			if len(parts) == 0 {
				return err
			}
			out["continuation_reason"] = mcptools.FailureDescription(videoInspectionError(err))
			break
		}
		stop := math.Min(end, next+math.Min(count, seconds))
		if stop <= next {
			return toolInputError("音轨读取未推进，已停止以保留可核对的起点")
		}
		part := map[string]any{"start_seconds": next, "end_seconds": stop, "text": text}
		if noSpeech {
			part["state"] = "no_recognizable_speech"
			part["provider_code"] = "ASR_RESPONSE_HAVE_NO_WORDS"
		} else {
			part["state"] = "transcribed"
		}
		encoded, _ := json.Marshal(part)
		if resultBytes+len(encoded)*2 > 800<<10 {
			out["continuation_reason"] = "本次结果接近1MiB工具结果传输封套；所需范围未缩短，请从next_start_seconds继续读取"
			break
		}
		resultBytes += len(encoded) * 2
		parts = append(parts, part)
		if text != "" {
			texts = append(texts, text)
		}
		next = stop
		if seconds < count-.1 {
			early = true
			break
		}
	}
	out["segments"], out["text"] = parts, strings.Join(texts, "\n")
	if len(texts) == 0 && len(parts) > 0 {
		out["transcription_state"] = "no_recognizable_speech"
		out["observation"] = "所选音轨范围已处理，识别服务未返回可转写的词；可能为音乐、环境声或不清晰人声，不代表提取或鉴权失败。"
	} else {
		out["transcription_state"] = "transcribed"
	}
	out["start_seconds"], out["end_seconds"], out["next_start_seconds"], out["complete"] = in.AudioStart, next, next, next >= total-.05 || early
	out["requested_range_complete"], out["video_audio_complete"] = next >= end-.0001 || early, next >= total-.05 || early
	out["audio_track_ended_early"], out["requested_duration_seconds"], out["effective_duration_seconds"] = early, in.AudioLength, length
	out["requested_audio_chunk_seconds"], out["effective_audio_chunk_seconds"], out["audio_timeout_seconds"] = requested, chunk, timeout
	if requested > chunk {
		out["audio_chunk_adjustment_reason"] = "16kHz mono PCM16 must fit the8MiB ASR transport; the full requested range is processed in multiple chunks"
	}
	return nil
}
