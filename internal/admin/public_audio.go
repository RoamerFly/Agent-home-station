package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/plugins"
)

func preparePublicAudio(ctx context.Context, dir string, data []byte) (plugins.Output, error) {
	return preparePublicAudioPreview(ctx, dir, data, 0)
}
func defaultAudioPreview(request string) int {
	if strings.Contains(request, "试听") {
		return 45
	}
	for _, p := range []string{"整首", "全曲", "完整", "全首", "发完"} {
		if strings.Contains(request, p) {
			return 0
		}
	}
	return 0
}
func preparePublicAudioPreview(ctx context.Context, dir string, data []byte, preview int) (plugins.Output, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	tmp, err := os.MkdirTemp(dir, "public-audio-")
	if err != nil {
		return plugins.Output{}, err
	}
	defer os.RemoveAll(tmp)
	input := filepath.Join(tmp, "input.bin")
	if err = os.WriteFile(input, data, 0600); err != nil {
		return plugins.Output{}, err
	}
	args := []string{"-v", "error", "-protocol_whitelist", "file", "-format_whitelist", "wav,mp3,flac,ogg,aac,mov,matroska,webm", "-show_entries", "format=duration", "-of", "json", input}
	b, err := exec.CommandContext(ctx, "ffprobe", args...).Output()
	if err != nil || len(b) > 65536 {
		return plugins.Output{}, &mcptools.WebError{Code: "audio_prepare_failed"}
	}
	var probe struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	_ = json.Unmarshal(b, &probe)
	seconds, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > 900 {
		return plugins.Output{}, &mcptools.WebError{Code: "audio_duration_limit"}
	}
	sendingSeconds := seconds
	if preview > 0 && sendingSeconds > float64(preview) {
		sendingSeconds = float64(preview)
	}
	chunks := int(math.Ceil(sendingSeconds / 60))
	output := plugins.Output{Text: fmt.Sprintf("真实音频已下载并转换，时长约 %.1f 秒，将由微信后台分为最多 60 秒一条的语音（约 %d 条）。尚未发送。", seconds, chunks)}
	if sendingSeconds < seconds {
		output.Text = fmt.Sprintf("原音频约 %.1f 秒；按要求准备 %.0f 秒试听，原音频完整保留，用户后续要文件可直接复用。不要主动询问文件。尚未发送。", seconds, sendingSeconds)
	}
	for i := 0; i < chunks; i++ {
		filename := fmt.Sprintf("audio-part-%d-of-%d.wav", i+1, chunks)
		dest := filepath.Join(tmp, filename)
		length := math.Min(60, sendingSeconds-float64(i*60))
		cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin", "-y", "-protocol_whitelist", "file", "-format_whitelist", "wav,mp3,flac,ogg,aac,mov,matroska,webm", "-threads", "1", "-ss", strconv.Itoa(i*60), "-i", input, "-vn", "-ac", "1", "-ar", "16000", "-t", formatSeconds(length), "-c:a", "pcm_s16le", "-f", "wav", dest)
		if err = cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return plugins.Output{}, ctx.Err()
			}
			return plugins.Output{}, &mcptools.WebError{Code: "audio_prepare_failed"}
		}
		part, err := os.ReadFile(dest)
		if err != nil || len(part) < 44 || len(part) > 10<<20 {
			return plugins.Output{}, &mcptools.WebError{Code: "audio_prepare_failed"}
		}
		output.Media = append(output.Media, plugins.MediaResult{Kind: "voice", Data: part, MIMEType: "audio/wav", Filename: filename})
	}
	return output, nil
}

func safeMediaFilename(name string) bool {
	return name != "" && len(name) <= 512 && !strings.ContainsAny(name, "/\\\x00\r\n")
}

func extractPublicAudioFile(ctx context.Context, dir string, data []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	tmp, err := os.MkdirTemp(dir, "public-audio-extract-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	input, output := filepath.Join(tmp, "source.mp4"), filepath.Join(tmp, "audio.m4a")
	if err = os.WriteFile(input, data, 0600); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin", "-y", "-protocol_whitelist", "file", "-format_whitelist", "mov", "-i", input, "-map", "0:a:0", "-vn", "-c:a", "copy", "-f", "ipod", output)
	if err = cmd.Run(); err != nil {
		return nil, &mcptools.WebError{Code: "audio_prepare_failed"}
	}
	info, err := os.Stat(output)
	if err != nil || info.Size() < 32 || info.Size() > toolsLimit("voice") {
		return nil, &mcptools.WebError{Code: "audio_prepare_failed"}
	}
	return os.ReadFile(output)
}
