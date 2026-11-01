package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Listening is optional model evidence. Its live task proof remains mandatory
// when staging the original recording, even if the model is unavailable.
func (p *permittedTools) reviewSongAudio(ctx context.Context, audio []byte, expected, jobID string, spec *songSpec, kind string) (map[string]any, func(context.Context) error) {
	origin, err := p.newMusicAudioOrigin(ctx, audio, expected, jobID, spec, kind)
	if err != nil {
		return map[string]any{"state": "failed", "reason": err.Error()}, func(context.Context) error { return err }
	}
	check := origin.Check
	if !p.manager.mediaAudioObservationConfigured(ctx) {
		return map[string]any{"state": "unavailable", "reason": "未配置或启用音频观察插件；当前成品尚未经模型听感检查"}, check
	}
	wav, windows, err := prepareSongAudioSamples(ctx, p.manager.dir, origin.audio)
	if err != nil {
		return map[string]any{"state": "failed", "reason": err.Error()}, check
	}
	policy, err := origin.observationPolicy(ctx, wav, windows)
	if err != nil {
		return map[string]any{"state": "failed", "reason": err.Error(), "windows": windows}, check
	}
	result, err := policy.observe(ctx)
	if err != nil {
		return map[string]any{"state": "failed", "reason": err.Error(), "model": policy.model, "windows": windows}, check
	}
	if err = policy.checkCompleted(ctx); err != nil {
		return map[string]any{"state": "failed", "reason": err.Error(), "windows": windows}, policy.checkCompleted
	}
	review := map[string]any{"state": result.Status, "model": result.Model, "windows": windows, "observation": result.Text, "expected_vocal_gender": expected, "voice_identity_evidence": result.VoiceEvidence, "instruction": "这是实际音频抽样的模型意见，不是全曲人工验收或声线认证。根据观察与用户需求判断能否交付；不要用请求参数代替实际观察。"}
	if result.ProviderModel != "" {
		review["provider_model"] = result.ProviderModel
	}
	if result.Status == "partial" {
		review["instruction"] = "本次音频观察因输出额度而截断，仅有部分模型意见；不是完整听感结论、全曲人工验收或声线认证，不得把partial标作observed。"
	}
	return review, policy.checkCompleted
}

func prepareSongAudioSamples(ctx context.Context, root string, audio []byte) ([]byte, []musicAudioWindow, error) {
	if len(audio) == 0 || len(audio) > 64<<20 {
		return nil, nil, errors.New("music_audio_source_limit_64mib")
	}
	tmp, err := os.MkdirTemp(root, "song-listening-")
	if err != nil {
		return nil, nil, errors.New("music_audio_samples_prepare_failed")
	}
	defer os.RemoveAll(tmp)
	source, target := filepath.Join(tmp, "source.audio"), filepath.Join(tmp, "samples.wav")
	if err = os.WriteFile(source, audio, 0600); err != nil {
		return nil, nil, errors.New("music_audio_samples_prepare_failed")
	}
	const formats = "wav,mp3,flac,ogg,aac,mov,matroska,webm"
	probe, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file", "-format_whitelist", formats, "-select_streams", "a:0", "-show_entries", "stream=codec_type:format=duration", "-of", "json", source).Output()
	var parsed struct {
		Streams []struct {
			Type string `json:"codec_type"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err != nil || len(probe) > 65536 || json.Unmarshal(probe, &parsed) != nil || len(parsed.Streams) != 1 || parsed.Streams[0].Type != "audio" {
		return nil, nil, errors.New("music_audio_source_decode_failed")
	}
	duration, err := strconv.ParseFloat(parsed.Format.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
		return nil, nil, errors.New("music_audio_source_duration_invalid")
	}
	windows := make([]musicAudioWindow, 0, 3)
	filters := []string{}
	labels := ""
	for i, fraction := range []float64{.15, .5, .8} {
		start := duration * fraction
		seconds := math.Min(15, duration-start)
		windows = append(windows, musicAudioWindow{Offset: start, Seconds: seconds})
		filters = append(filters, fmt.Sprintf("[0:a]atrim=start=%.9f:duration=%.9f,asetpts=PTS-STARTPTS[a%d]", start, seconds, i))
		labels += fmt.Sprintf("[a%d]", i)
	}
	filters = append(filters, labels+"concat=n=3:v=0:a=1[out]")
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin", "-y", "-protocol_whitelist", "file", "-format_whitelist", formats, "-threads", "1", "-i", source, "-filter_complex", strings.Join(filters, ";"), "-map", "[out]", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-fs", strconv.Itoa(3<<20), target)
	if err = cmd.Run(); err != nil {
		return nil, nil, errors.New("music_audio_samples_decode_failed")
	}
	info, err := os.Stat(target)
	if err != nil || info.Size() < 44 || info.Size() > 3<<20 {
		return nil, nil, errors.New("music_audio_samples_wav_limit_3mib")
	}
	wav, err := os.ReadFile(target)
	if err != nil {
		return nil, nil, errors.New("music_audio_samples_unreadable")
	}
	return wav, windows, nil
}
