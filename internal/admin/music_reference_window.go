package admin

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func prepareReferenceClip(ctx context.Context, root string, data []byte, start, seconds float64) ([]byte, float64, error) {
	dir, err := os.MkdirTemp(root, "reference-clip-")
	if err != nil {
		return nil, 0, err
	}
	defer os.RemoveAll(dir)
	source, target := filepath.Join(dir, "source.audio"), filepath.Join(dir, "reference.m4a")
	if err = os.WriteFile(source, data, 0600); err != nil {
		return nil, 0, err
	}
	args := []string{"-v", "error", "-nostdin", "-y", "-protocol_whitelist", "file", "-i", source, "-ss", strconv.FormatFloat(start, 'f', -1, 64), "-t", strconv.FormatFloat(seconds, 'f', -1, 64), "-vn", "-ac", "2", "-ar", "44100", "-c:a", "aac", "-b:a", "192k", target}
	if exec.CommandContext(ctx, "ffmpeg", args...).Run() != nil {
		return nil, 0, toolInputError("参考片段截取失败，请核对音频格式、起点和时长；本次未上传或生成")
	}
	probe, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", target).Output()
	actual, parseErr := strconv.ParseFloat(strings.TrimSpace(string(probe)), 64)
	if err != nil || parseErr != nil || actual+.1 < seconds {
		return nil, 0, toolInputError("原音频不足所选参考范围；请调整起点或时长，未缩短显式请求、本次未上传或生成")
	}
	clip, err := os.ReadFile(target)
	if err != nil || len(clip) < 44 || len(clip) > 25<<20 {
		return nil, 0, toolInputError("截取后的参考音频不可读或超过上传大小；可选择更短片段，本次未上传或生成")
	}
	return clip, actual, nil
}

// The default follows the same audio-bound lyric evidence as the submitted
// words, rather than the opening of a concert recording. Explicit zero and
// other caller selections are preserved.
func (p *permittedTools) referenceWindow(a mediaAsset, ids []string, target int, start, seconds *float64) (float64, float64, error) {
	from, length := 0.0, float64(target)
	earliest, end := math.Inf(1), 0.0
	for _, id := range ids {
		b, err := os.ReadFile(filepath.Join(p.manager.dir, "music-lyrics", id+".json"))
		var e lyricEvidence
		if err != nil || json.Unmarshal(b, &e) != nil || !p.lyricEvidenceMatches(a, e) || e.Seconds <= 0 {
			continue
		}
		earliest = math.Min(earliest, float64(e.Offset))
		end = math.Max(end, float64(e.Offset+e.Seconds))
	}
	if !math.IsInf(earliest, 1) {
		from = earliest
		length = math.Min(length, end-earliest)
	}
	if start != nil {
		from = *start
	}
	if seconds != nil {
		length = *seconds
	}
	if math.IsNaN(from) || math.IsInf(from, 0) || from < 0 || math.IsNaN(length) || math.IsInf(length, 0) || length <= 0 || length > 600 {
		return 0, 0, toolInputError("参考起点须为非负秒数，单段参考时长须大于0且不超过本地工作流600秒窗口；请按所需片段分次处理")
	}
	return from, length, nil
}
