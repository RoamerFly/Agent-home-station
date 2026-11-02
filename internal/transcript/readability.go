// Package transcript provides lossless display formatting for ASR text. It
// never corrects recognized words, assigns speakers, or invokes another model.
package transcript

import (
	"strings"
	"unicode"
)

// Readable adds conservative clause punctuation and paragraph breaks to long
// unpunctuated Chinese transcripts. Existing punctuation and quoted text stay
// intact. Inserted punctuation is a reading aid, not an ASR timing annotation.
// The original provider result remains in the plugin's verified private job.
func Readable(raw string) string {
	runes := []rune(raw)
	if len(runes) < 32 {
		return raw
	}
	han := 0
	for _, r := range runes {
		if unicode.Is(unicode.Han, r) {
			han++
		}
	}
	if han*2 < len(runes) {
		return raw
	}
	var b strings.Builder
	line, clause := 0, 0
	var close rune
	for i, r := range runes {
		remaining := string(runes[i:min(i+6, len(runes))])
		enumeration := false
		for _, prefix := range []string{"第一个", "第二个", "第三个", "第四个", "第五个", "第六个", "第七个", "第八个", "第九个", "第十个", "第一点", "第二点", "第三点", "第四点", "第五点", "第六点", "下一个", "下一点"} {
			if strings.HasPrefix(remaining, prefix) {
				enumeration = true
				break
			}
		}
		cue := false
		for _, prefix := range []string{"因为", "所以", "但是", "不过", "比如", "例如", "而且", "然后", "也就是说"} {
			if strings.HasPrefix(remaining, prefix) {
				cue = true
				break
			}
		}
		if close == 0 && i > 0 && !unicode.IsSpace(runes[i-1]) {
			if enumeration && line >= 8 {
				if !unicode.IsPunct(runes[i-1]) {
					b.WriteRune('。')
				}
				b.WriteRune('\n')
				line, clause = 0, 0
			} else if cue && clause >= 12 && !unicode.IsPunct(runes[i-1]) {
				b.WriteRune('，')
				clause = 0
			} else if line >= 80 {
				// Without timing/grammar evidence, wrap rather than invent a
				// sentence boundary in the middle of an unrecognized clause.
				b.WriteRune('\n')
				line = 0
			}
		}
		b.WriteRune(r)
		line++
		clause++
		if unicode.IsPunct(r) {
			clause = 0
		}
		if r == '\n' {
			line, clause = 0, 0
		}
		if close != 0 {
			if r == close {
				close = 0
			}
		} else {
			switch r {
			case '“':
				close = '”'
			case '「':
				close = '」'
			case '『':
				close = '』'
			case '"':
				close = '"'
			}
		}
	}
	return b.String()
}
