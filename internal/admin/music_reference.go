package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mediaspec"
)

// Parameters are attached to the generated asset, not a global most-recent song.
// They describe what was submitted, not a guarantee that the singer followed it.
type songSpec struct {
	VoiceID          string        `json:"voice_id,omitempty"`
	VoiceProfile     string        `json:"voice_profile_id,omitempty"`
	Engine           string        `json:"engine,omitempty"`
	Sampling         musicSampling `json:"sampling,omitempty"`
	PlanningMode     string        `json:"planning_mode,omitempty"`
	SingingVoice     string        `json:"singing_voice,omitempty"`
	VocalGender      string        `json:"vocal_gender,omitempty"`
	Mode             string        `json:"mode"`
	Style            string        `json:"style"`
	Lyrics           string        `json:"lyrics"`
	Seed             int64         `json:"seed"`
	Duration         int           `json:"duration_seconds"`
	Reference        string        `json:"reference_asset_id,omitempty"`
	ReferenceMode    string        `json:"reference_mode,omitempty"`
	LyricEvidence    []string      `json:"lyrics_evidence_ids,omitempty"`
	ReferenceHash    string        `json:"reference_sha256,omitempty"`
	ReferenceStart   float64       `json:"reference_start_seconds,omitempty"`
	ReferenceSeconds float64       `json:"reference_seconds,omitempty"`
	TextGuidance     float64       `json:"text_guidance_scale,omitempty"`
	MinimumDuration  float64       `json:"min_duration_seconds,omitempty"`
}

func (p *permittedTools) referenceAsset(id string) (mediaAsset, []byte, error) {
	m := p.manager
	m.assetMu.Lock()
	defer m.assetMu.Unlock()
	items, err := m.loadAssets()
	if err != nil {
		return mediaAsset{}, nil, err
	}
	for _, a := range items {
		if a.ID != id || a.Account != p.trace.Account || a.Scope != assetScope(p.trace) && !librarySong(a, p.trace.Account) {
			continue
		}
		if !strings.HasPrefix(a.MIME, "audio/") || a.Hash == "" {
			return a, nil, toolInputError("参考素材须为已保存的原音频；请用music_library_search或media_recent选择音频asset_id；本次未提交生成")
		}
		b, err := m.cachedAssetData(a)
		if err != nil || len(b) == 0 || len(b) > 25<<20 {
			return a, nil, toolInputError("参考音频缓存不可用、校验失败或超过25MiB；请核对原素材并准备有效参考；本次未提交生成")
		}
		return a, b, nil
	}
	return mediaAsset{}, nil, toolInputError("未找到参考音频；请先用music_library_search和media_recent核对本地原曲；本次未提交生成")
}

func (p *permittedTools) inspectMusicReference(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		ID         string   `json:"asset_id"`
		Transcribe bool     `json:"transcribe"`
		Offset     int      `json:"offset_seconds"`
		Seconds    int      `json:"seconds"`
		Supplied   string   `json:"supplied_lyrics"`
		Evidence   []string `json:"lyrics_evidence_ids"`
	}
	if json.Unmarshal(raw, &input) != nil || input.ID == "" || len(input.ID) > 128 || input.Offset < 0 || input.Offset > 600 || input.Seconds < 0 || input.Seconds > 60 {
		return "", errors.New("参考音频检查参数无效")
	}
	if !p.manager.Config().Companion.Permissions.Remember {
		return "", errors.New("本地素材读取权限未开启")
	}
	a, data, err := p.referenceAsset(input.ID)
	if err != nil {
		return "", err
	}
	out := map[string]any{"asset_id": a.ID, "title": a.Title, "source_url": a.URL, "cached_mime": a.MIME, "bytes": len(data), "generation": a.Music, "instruction": "已读取本地音频。先核对歌手、版本和演唱片段；generation 是原提交配方。music_local_start 用 reference_start_seconds/reference_seconds 选原录音区间；省略起点沿用歌词依据最早位置，显式 0 保留开头。重发原版用 media_reuse。"}
	out["available_lyrics"] = p.availableLyricEvidence(a)
	if a.Music != nil {
		out["lyrics_evidence_ids"] = a.Music.LyricEvidence
		out["lyric_timing_review"] = p.lyricTimingFor(a, a.Music.LyricEvidence)
	}
	if len(input.Evidence) > 0 {
		out["lyric_timing_review"] = p.lyricTimingFor(a, input.Evidence)
	}
	if input.Supplied != "" {
		request := companion.UserRequestText(p.trace.Text)
		if len(input.Supplied) > 6000 || !strings.Contains(request, input.Supplied) {
			return "", errors.New("登记的歌词必须直接来自当前用户正文，不能把模型编写或历史改编歌词当用户原词")
		}
		e, err := p.saveLyricEvidence(a, input.Supplied, "user_supplied", 0, 0)
		if err != nil {
			return "", err
		}
		out["lyrics_evidence_id"], out["lyrics"] = e.ID, e.Text
	}
	if input.Transcribe {
		seconds := input.Seconds
		if seconds == 0 {
			seconds = 60
		}
		wav, err := musicTranscriptionWindow(ctx, p.manager.dir, data, input.Offset, seconds)
		if err != nil {
			return "", err
		}
		text, backend, check, err := p.transcribeReference(ctx, a, wav, input.Offset, seconds)
		noSpeech := mediaspec.IsNoSpeech(err)
		if err != nil && !noSpeech {
			return "", err
		}
		out["offset_seconds"], out["window_seconds"] = input.Offset, seconds
		out["transcription_backend"] = backend
		if duration, e := wavDuration(wav); e == nil {
			out["actual_window_seconds"] = duration
		}
		if err = check(ctx); err != nil {
			return "", err
		}
		if noSpeech {
			out["transcription_status"] = "no_recognizable_speech"
			out["transcript_warning"] = "本段未识别到可辨歌词。可选另一段演唱音频，或先分离人声再转写。"
			b, _ := json.Marshal(out)
			return string(b), nil
		}
		out["transcript"] = text
		out["transcription_status"] = "transcribed"
		out["reference_window_candidate"] = map[string]any{"reference_start_seconds": input.Offset, "reference_seconds": seconds, "instruction": "这是本次实际识别的区间；核对是否为目标歌曲演唱，ASR候选不等于音质或人声质量认证"}
		e, err := p.saveLyricEvidence(a, text, "asr_candidate", input.Offset, seconds)
		if err != nil {
			return "", err
		}
		out["lyrics_evidence_id"] = e.ID
		out["lyric_timing_review"] = lyricTimingReview([]lyricEvidence{e})
		out["transcript_warning"] = "这是指定片段的ASR候选歌词，可能漏字、错字，不保证完整准确；不要声称已精确识别或已还原。按需分段检查，仍不确定时确认歌词，不用原创词替代。"
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (p *permittedTools) transcribeReference(ctx context.Context, asset mediaAsset, wav []byte, offset, seconds int) (string, string, func(context.Context) error, error) {
	if station := p.manager.readyStationASR(ctx); station != nil {
		_, check, err := p.newReferenceAudioOrigin(ctx, asset, offset, seconds, "transcribe selected reference recording", "reference-asr")
		if err != nil {
			return "", "", nil, err
		}
		text, err := station.transcribe(ctx, wav, check)
		return text, "station_local_asr", check, err
	}
	policy, err := p.newReferenceTranscriptionPolicy(ctx, asset, wav, offset, seconds)
	if err != nil {
		return "", "", nil, err
	}
	text, err := policy.transcribe(ctx)
	return text, policy.Backend(), policy.checkCompleted, err
}

func musicTranscriptionWindow(ctx context.Context, root string, data []byte, offset, seconds int) ([]byte, error) {
	dir, err := os.MkdirTemp(root, "music-reference-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in, out := filepath.Join(dir, "input.audio"), filepath.Join(dir, "window.wav")
	if err = os.WriteFile(in, data, 0600); err != nil {
		return nil, err
	}
	args := []string{"-v", "error", "-nostdin", "-y", "-protocol_whitelist", "file", "-i", in, "-ss", strconv.Itoa(offset), "-t", strconv.Itoa(seconds), "-vn", "-ac", "1", "-ar", "16000", out}
	if exec.CommandContext(ctx, "ffmpeg", args...).Run() != nil {
		return nil, errors.New("参考音频片段解码失败")
	}
	b, err := os.ReadFile(out)
	if err != nil || len(b) < 44 || len(b) > 3<<20 {
		return nil, errors.New("参考音频片段为空或超过限制")
	}
	return b, nil
}
