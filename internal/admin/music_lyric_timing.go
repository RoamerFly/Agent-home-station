package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// Text equality proves neither word timing nor lyric-to-melody alignment.
// Return observations without deleting repeated choruses or rewriting words.
func lyricTimingReview(items []lyricEvidence) map[string]any {
	ranges := []map[string]any{}
	repeats := []map[string]any{}
	timed := []lyricEvidence{}
	outOfOrder := false
	for i, e := range items {
		ranges = append(ranges, map[string]any{"lyrics_evidence_id": e.ID, "start_seconds": e.Offset, "end_seconds": e.Offset + e.Seconds, "has_audio_timing": e.Seconds > 0})
		if e.Seconds > 0 {
			timed = append(timed, e)
		}
		if i == 0 {
			continue
		}
		prev := items[i-1]
		if e.Seconds > 0 && prev.Seconds > 0 && e.Offset < prev.Offset {
			outOfOrder = true
		}
		a, b := []rune(lyricText(prev.Text)), []rune(lyricText(e.Text))
		limit := len(a)
		if len(b) < limit {
			limit = len(b)
		}
		for n := limit; n >= 6; n-- {
			if string(a[len(a)-n:]) == string(b[:n]) {
				repeats = append(repeats, map[string]any{"before": prev.ID, "after": e.ID, "repeated_characters": n})
				break
			}
		}
	}
	sort.Slice(timed, func(i, j int) bool { return timed[i].Offset < timed[j].Offset })
	covered, gaps, overlap, end := 0, 0, 0, -1
	for _, e := range timed {
		start, stop := e.Offset, e.Offset+e.Seconds
		if end < 0 {
			covered += e.Seconds
			end = stop
			continue
		}
		if start > end {
			gaps += start - end
		} else {
			n := end - start
			if n > e.Seconds {
				n = e.Seconds
			}
			overlap += n
		}
		if stop > end {
			begin := start
			if end > begin {
				begin = end
			}
			covered += stop - begin
			end = stop
		}
	}
	return map[string]any{"evidence_ranges": ranges, "audio_coverage_seconds": covered, "gap_seconds": gaps, "overlap_seconds": overlap, "out_of_order": outOfOrder, "boundary_repetitions": repeats, "word_timestamps_verified": false, "melody_alignment_verified": false, "instruction": "字词匹配不等于唱词节拍匹配。以上是ASR粗窗口，重复边界可能是跨窗重复或真实复唱；不能自动删词。先用更短、不重叠窗口核对乐句起止及顺序，保留原曲分行/段落并与选定参考一致；不要把不同版本或半首词拼到另一段乐谱。"}
}

func (p *permittedTools) lyricTimingFor(a mediaAsset, ids []string) map[string]any {
	items := []lyricEvidence{}
	for _, id := range ids {
		if !validID(id) || len(id) != 64 {
			continue
		}
		data, e := os.ReadFile(filepath.Join(p.manager.dir, "music-lyrics", id+".json"))
		var item lyricEvidence
		if e == nil && json.Unmarshal(data, &item) == nil && p.lyricEvidenceMatches(a, item) {
			items = append(items, item)
		}
	}
	return lyricTimingReview(items)
}
