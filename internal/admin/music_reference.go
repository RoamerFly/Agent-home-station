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
	"wechat-bot/internal/bailian"
	"wechat-bot/internal/companion"
)

// Parameters are attached to the generated asset, not a global most-recent song.
// They describe what was submitted, not a guarantee that the singer followed it.
type songSpec struct {
	SingingVoice  string   `json:"singing_voice,omitempty"`
	VocalGender   string   `json:"vocal_gender,omitempty"`
	Mode          string   `json:"mode"`
	Style         string   `json:"style"`
	Lyrics        string   `json:"lyrics"`
	Seed          int64    `json:"seed"`
	Duration      int      `json:"duration_seconds"`
	Reference     string   `json:"reference_asset_id,omitempty"`
	ReferenceMode string   `json:"reference_mode,omitempty"`
	LyricEvidence []string `json:"lyrics_evidence_ids,omitempty"`
	ReferenceHash string   `json:"reference_sha256,omitempty"`
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
		if a.ID != id || a.Account != p.trace.Account || a.Scope != assetScope(p.trace) {
			continue
		}
		if !strings.HasPrefix(a.MIME, "audio/") || a.Hash == "" {
			return a, nil, toolInputError("参考素材必须是本会话缓存的原音频；请用media_recent选择实际音频asset_id；本次未提交生成")
		}
		b, err := m.cachedAssetData(a)
		if err != nil || len(b) == 0 || len(b) > 25<<20 {
			return a, nil, toolInputError("参考音频缓存不可用、校验失败或超过25MiB；请核对原素材并准备有效参考；本次未提交生成")
		}
		return a, b, nil
	}
	return mediaAsset{}, nil, toolInputError("本会话找不到参考音频；请先用media_recent核对引用或下载真实原曲；本次未提交生成")
}

func (p *permittedTools) inspectMusicReference(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		ID         string `json:"asset_id"`
		Transcribe bool   `json:"transcribe"`
		Offset     int    `json:"offset_seconds"`
		Seconds    int    `json:"seconds"`
		Supplied   string `json:"supplied_lyrics"`
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
	out := map[string]any{"asset_id": a.ID, "title": a.Title, "source_url": a.URL, "cached_mime": a.MIME, "bytes": len(data), "generation": a.Music, "instruction": "这是同一会话已校验的原音频。generation如存在是当时提交的风格、歌词和种子；外部原曲不能编造歌词。翻唱应以这份原曲为reference_asset_id；重发原版用media_reuse，不重新生成。参考生成会重新合成伴奏，不是精确换声或原伴奏保留。"}
	out["available_lyrics"] = p.availableLyricEvidence(a)
	if a.Music != nil {
		out["lyrics_evidence_ids"] = a.Music.LyricEvidence
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
		c := p.manager.speechConfig()
		if !c.ASREnabled {
			return "", errors.New("歌词转写需要已配置并启用语音识别；不能自行编歌词冒充原词")
		}
		seconds := input.Seconds
		if seconds == 0 {
			seconds = 60
		}
		wav, err := musicTranscriptionWindow(ctx, p.manager.dir, data, input.Offset, seconds)
		if err != nil {
			return "", err
		}
		text, err := bailian.New(c).Transcribe(ctx, wav)
		if err != nil {
			return "", err
		}
		out["transcript"], out["offset_seconds"], out["window_seconds"] = text, input.Offset, seconds
		e, err := p.saveLyricEvidence(a, text, "asr_candidate", input.Offset, seconds)
		if err != nil {
			return "", err
		}
		out["lyrics_evidence_id"] = e.ID
		out["transcript_warning"] = "这是指定片段的ASR候选歌词，可能漏字、错字，不保证完整准确；不要声称已精确识别或已还原。按需分段检查，仍不确定时确认歌词，不用原创词替代。"
	}
	b, _ := json.Marshal(out)
	return string(b), nil
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
