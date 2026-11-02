package companion

import "strings"

// SavedVideoTranscript returns the existing transcript section without
// re-summarizing or re-running ASR. False means this observation has no such
// section, not that the underlying video has no audio.
func SavedVideoTranscript(observation string) (string, bool) {
	for _, part := range videoEvidenceParts(observation) {
		if part.kind == "media_transcript" {
			return part.text, true
		}
	}
	return "", false
}

// Separate modalities without summarizing, shortening or inventing coverage.
// A sampled visual observation must not downgrade the independently returned
// audio transcript. Source identity and versions remain on every resulting row.
func videoEvidenceParts(text string) []struct{ kind, text string } {
	if !strings.Contains(text, "[微信视频") {
		return nil
	}
	audio := strings.Index(text, "[实际音轨转写")
	if audio < 0 {
		return nil
	}
	end := strings.Index(text[audio:], "]")
	if end < 0 || strings.TrimSpace(text[audio+end+1:]) == "" {
		return nil
	}
	parts := []struct{ kind, text string }{}
	if header := strings.Index(text, "[微信视频"); header > 0 {
		parts = append(parts, struct{ kind, text string }{"media_source_description", strings.TrimSpace(text[:header])})
	}
	visual := strings.Index(text, "[画面观察")
	if visual >= 0 && visual < audio {
		parts = append(parts, struct{ kind, text string }{"media_visual_observation", strings.TrimSpace(text[visual:audio])})
	}
	parts = append(parts, struct{ kind, text string }{"media_transcript", strings.TrimSpace(text[audio:])})
	return parts
}
