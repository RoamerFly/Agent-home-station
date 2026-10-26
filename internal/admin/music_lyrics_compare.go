package admin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Semi-global edit distance aligns an ASR WINDOW to a candidate's corresponding
// passage. The entire song must never replace a 30-second transcription.
func alignLyricWindow(transcript, candidate string) (string, int, float64) {
	a := []rune(lyricText(transcript))
	raw := []rune(lyricSection.ReplaceAllString(candidate, ""))
	b := []rune{}
	positions := []int{}
	for i, r := range raw {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b = append(b, unicode.ToLower(r))
			positions = append(positions, i)
		}
	}
	if len(a) < 16 || len(a) > 2000 || len(b) == 0 || len(b) > 6000 {
		return "", 0, 0
	}
	previous := make([]int, len(b)+1)
	starts := make([]int, len(b)+1)
	for j := range starts {
		starts[j] = j
	}
	for i := 1; i <= len(a); i++ {
		row := make([]int, len(b)+1)
		begin := make([]int, len(b)+1)
		row[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			row[j], begin[j] = previous[j-1]+cost, starts[j-1]
			if previous[j]+1 < row[j] {
				row[j], begin[j] = previous[j]+1, starts[j]
			}
			if row[j-1]+1 < row[j] {
				row[j], begin[j] = row[j-1]+1, begin[j-1]
			}
		}
		previous, starts = row, begin
	}
	end := 1
	for j := 2; j <= len(b); j++ {
		if previous[j] < previous[end] {
			end = j
		}
	}
	start := starts[end]
	if start >= end {
		return "", previous[end], 0
	}
	size := len(a)
	if end-start > size {
		size = end - start
	}
	score := 1 - float64(previous[end])/float64(size)
	return strings.TrimSpace(string(raw[positions[start] : positions[end-1]+1])), previous[end], score
}

func pageContainsLyrics(page, lyrics string) bool {
	// Ignore web formatting/line breaks, but require actual contiguous words.
	normalized := lyricText(lyrics)
	return normalized != "" && strings.Contains(lyricText(page), normalized)
}

func (p *permittedTools) compareMusicLyrics(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Asset    string `json:"asset_id"`
		Evidence string `json:"lyrics_evidence_id"`
		URL      string `json:"candidate_url"`
		Lyrics   string `json:"candidate_lyrics"`
	}
	if json.Unmarshal(raw, &in) != nil || len(in.Evidence) != 64 || strings.Trim(in.Evidence, "0123456789abcdef") != "" || len(in.Lyrics) > 6000 || in.Lyrics == "" || len(in.URL) > 2048 {
		return "", errors.New("请提供原曲、真实转写依据、候选网页和该版本歌词")
	}
	a, _, err := p.referenceAsset(in.Asset)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(p.manager.dir, "music-lyrics", in.Evidence+".json"))
	var base lyricEvidence
	if err != nil || len(b) > 16<<10 || json.Unmarshal(b, &base) != nil || base.ID != in.Evidence || base.Source != "asr_candidate" || !p.lyricEvidenceMatches(a, base) {
		return "", errors.New("只能核对所选原曲的实际ASR候选，不能用模型写词或再次修正过的词替代原始转写")
	}
	args, _ := json.Marshal(map[string]string{"url": in.URL})
	page, err := p.Call(ctx, "web_fetch", args)
	if err != nil {
		return "", err
	}
	if !pageContainsLyrics(page, in.Lyrics) {
		return "", errors.New("候选歌词未实际出现在读取的网页正文中；未创建歌词依据")
	}
	corrected, distance, score := alignLyricWindow(base.Text, in.Lyrics)
	threshold := int(math.Floor(float64(len([]rune(lyricText(base.Text)))) * .12))
	if threshold > 12 {
		threshold = 12
	}
	if threshold < 1 {
		threshold = 1
	}
	out := map[string]any{"asr_lyrics": base.Text, "candidate_url": in.URL, "matched_lyrics": corrected, "similarity": score, "edit_count": distance, "accepted": false, "instruction": "逐个比较不同版本，不要把整首候选塞入转写片段。词相似不能证明原唱或编曲版本一致，仍需核对歌名、歌手及实际参考录音。"}
	if corrected != "" && score >= .88 && distance <= threshold {
		proof, err := p.saveLyricEvidenceDetails(a, corrected, "web_aligned_asr", base.Offset, base.Seconds, in.URL, base.ID)
		if err != nil {
			return "", err
		}
		out["accepted"], out["lyrics_evidence_id"], out["lyrics"] = true, proof.ID, proof.Text
		out["instruction"] = "候选网页与本段ASR大部分一致，已按小差异生成修正依据；这不是人工校对证明。使用此lyrics_evidence_id及返回lyrics，继续核对其他片段和版本；不能加入未匹配的整首歌词。"
	} else {
		out["reason"] = "version_mismatch_or_uncertain_transcript"
		out["instruction"] = "差异过大、片段过短或无法对齐，未采纳此版本。继续找另一版本，或重转写更清楚的片段；不能强套候选。"
	}
	return stringMustJSON(out), nil
}
