package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"wechat-bot/internal/bailian"
)

func (p *permittedTools) reviewSongAudio(ctx context.Context, audio []byte, expected string) map[string]any {
	c := p.manager.speechConfig()
	if c.Key == "" {
		return map[string]any{"state": "unavailable", "reason": "未配置音频理解凭据；演唱性别尚未经听感检查"}
	}
	hash := sha256.New()
	hash.Write([]byte("song-listening-v1|" + expected + "|" + bailian.AudioReviewModel + "|"))
	hash.Write(audio)
	hash.Write([]byte(c.Endpoint))
	dir := filepath.Join(p.manager.dir, "music-audio-reviews")
	path := filepath.Join(dir, hex.EncodeToString(hash.Sum(nil))+".json")
	var review map[string]any
	if b, e := os.ReadFile(path); e == nil && json.Unmarshal(b, &review) == nil {
		return review
	}
	tmp, e := os.MkdirTemp(p.manager.dir, "song-listening-")
	if e != nil {
		return map[string]any{"state": "failed", "reason": "无法准备听感检查"}
	}
	defer os.RemoveAll(tmp)
	source, target := filepath.Join(tmp, "source.audio"), filepath.Join(tmp, "samples.wav")
	if os.WriteFile(source, audio, 0600) != nil {
		return map[string]any{"state": "failed", "reason": "无法准备听感检查"}
	}
	probe, e := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", source).Output()
	duration, parse := strconv.ParseFloat(strings.TrimSpace(string(probe)), 64)
	if e != nil || parse != nil || duration <= 0 {
		return map[string]any{"state": "failed", "reason": "无法读取成品时长"}
	}
	windows := []map[string]float64{}
	filters := []string{}
	labels := ""
	for i, f := range []float64{.15, .5, .8} {
		start := duration * f
		seconds := 15.0
		if start+seconds > duration {
			seconds = duration - start
		}
		windows = append(windows, map[string]float64{"offset_seconds": start, "seconds": seconds})
		filters = append(filters, fmt.Sprintf("[0:a]atrim=start=%f:duration=%f,asetpts=PTS-STARTPTS[a%d]", start, seconds, i))
		labels += fmt.Sprintf("[a%d]", i)
	}
	filters = append(filters, labels+"concat=n=3:v=0:a=1[out]")
	if exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin", "-y", "-i", source, "-filter_complex", strings.Join(filters, ";"), "-map", "[out]", "-ac", "1", "-ar", "16000", target).Run() != nil {
		return map[string]any{"state": "failed", "reason": "听感检查片段解码失败"}
	}
	wav, e := os.ReadFile(target)
	if e != nil {
		return map[string]any{"state": "failed", "reason": "听感检查片段不可读"}
	}
	text, e := bailian.New(c).ReviewAudio(ctx, wav, "这段音频由同一首歌前、中、后三处15秒片段按顺序拼接。仅依据实际听到的声音，判断各段主唱是男声、女声、混声还是无人声，是否是真实歌唱、是否明显失真。不要猜测目标，不要输出歌词。给出简短中文结论和置信度；无法确认须明确说明。")
	if e != nil {
		return map[string]any{"state": "failed", "reason": e.Error(), "model": bailian.AudioReviewModel, "windows": windows}
	}
	review = map[string]any{"state": "observed", "model": bailian.AudioReviewModel, "windows": windows, "observation": text, "expected_vocal_gender": expected, "instruction": "这是实际音频抽样的模型听感，不是全曲人工验收。根据观察与用户需求判断能否交付；明显男声/无人声/混声不应宣称为女声成品。不要用请求参数代替实际观察。"}
	if os.MkdirAll(dir, 0700) == nil {
		b, _ := json.Marshal(review)
		_ = writePrivate(path, b)
	}
	return review
}
