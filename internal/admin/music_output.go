package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/plugins"
)

func songTitle(title string) string {
	title = strings.TrimSpace(strings.Trim(title, "《》\""))
	var out []rune
	for _, r := range title {
		if !unicode.IsControl(r) && !strings.ContainsRune(`/\:*?"<>|`, r) {
			out = append(out, r)
		}
	}
	title = strings.Trim(strings.TrimSpace(string(out)), ". ")
	if len(out) > 24 {
		title = string(out[:24])
	}
	for _, s := range []string{"鲸鱼娘原创歌曲", "鲸鱼娘的原创歌曲", "原创歌曲", "歌曲名", "未命名"} {
		if title == s {
			return ""
		}
	}
	return title
}

func (p *permittedTools) stageSongOutput(ctx context.Context, audio []byte, typ, title, kind string) (string, error) {
	return p.stageSongOutputSpec(ctx, audio, typ, title, kind, nil)
}
func (p *permittedTools) stageSongOutputSpec(ctx context.Context, audio []byte, typ, title, kind string, spec *songSpec) (string, error) {
	if !p.musicKindPermitted(kind) {
		return "", errors.New("歌曲发送格式未授权")
	}
	title = songTitle(title)
	if title == "" {
		title = "晚风里的旋律"
	} // Legacy jobs have no title field.
	ext := ".flac"
	if strings.Contains(typ, "mpeg") {
		ext = ".mp3"
	} else if strings.Contains(typ, "wav") {
		ext = ".wav"
	}
	prefix, label := "大肥鱼原创歌曲-", "原创歌曲"
	if spec != nil && spec.Mode == "cover" {
		prefix, label = "大肥鱼翻唱-", "参考翻唱"
	}
	filename := prefix + title + ext
	out := plugins.Output{Text: label + "《" + title + "》已生成，尚未发送"}
	if kind == "voice" || kind == "both" {
		voices, err := preparePublicAudioPreview(ctx, p.manager.dir, audio, 0)
		if err != nil {
			return "", err
		}
		for i, v := range voices.Media {
			v.Filename = fmt.Sprintf("%s%s-语音%d%s", prefix, title, i+1, filepath.Ext(v.Filename))
			out.Media = append(out.Media, v)
		}
	}
	if kind == "file" || kind == "both" {
		out.Media = append(out.Media, plugins.MediaResult{Kind: "file", Data: audio, MIMEType: typ, Filename: filename})
	}
	original := &assetOriginal{VoiceComplete: kind == "voice" || kind == "both", Data: audio, MIME: typ, Filename: filename, Music: spec}
	if spec != nil && spec.Engine == "rvc" {
		original.VoiceID = spec.VoiceID
		original.VoiceEngine = "RVC"
	}
	result, err := p.stageOutputWithAsset(ctx, out, original)
	if err != nil || kind != "both" {
		return result, err
	}
	var staged struct {
		Media []companion.GeneratedMedia `json:"media"`
	}
	if json.Unmarshal([]byte(result), &staged) != nil {
		return "", errors.New("歌曲附件结果无效")
	}
	ids := []string{}
	for _, m := range staged.Media {
		ids = append(ids, m.ID)
	}
	var value map[string]any
	_ = json.Unmarshal([]byte(result), &value)
	value["required_media_ids"] = ids
	value["delivery_kind"] = "both"
	b, _ := json.Marshal(value)
	return string(b), nil
}
