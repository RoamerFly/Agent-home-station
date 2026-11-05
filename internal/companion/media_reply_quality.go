package companion

import "strings"

// This is an output-style check, not a truth filter or a replacement summary.
// Keep operational reading/review commentary in diagnostics. Legitimate quoted
// user requests about the reading process are exempt so they remain answerable.
func mediaReplyQualityIssues(in Input, d Decision) []GroundingBubbleVerdict {
	if len(in.IncomingMedia) == 0 {
		return nil
	}
	// in.Text may include the machine observation itself. Only a real user
	// request can exempt a diagnostic response; media text cannot self-exempt.
	literal, _ := literalInput(in)
	for _, question := range []string{"读取范围", "采样", "核验", "为什么失败", "什么错误", "读取进度", "逐字复述", "原样引用"} {
		if strings.Contains(literal, question) {
			return nil
		}
	}
	var issues []GroundingBubbleVerdict
	for i, text := range d.Messages {
		bad := false
		for _, phrase := range []string{"不把零星", "只拿到采样概览", "逐段核对", "证据摘录", "逐句来源核验", "不把片段当成"} {
			bad = bad || strings.Contains(text, phrase)
		}
		// "先不把盐放进锅里" can be genuine cooking content. Restrict this
		// known pattern to meta commentary, not arbitrary words from a video.
		if strings.Contains(text, "先不把") && (strings.Contains(text, "结论") || strings.Contains(text, "字幕") || strings.Contains(text, "片段")) {
			bad = true
		}
		if bad {
			issues = append(issues, GroundingBubbleVerdict{Index: i, Verdict: "reject", Violations: []GroundingViolation{{Span: text, Kind: "media_process_commentary", SourceRefs: []string{}, Reason: "Reply with the supported video content in natural language. Keep sampling, checking and self-justification in diagnostics. Preserve useful verified content; do not invent missing points or replace a summary with a reading-process disclaimer. If no useful content exists, leave the task incomplete rather than pretending a summary was delivered."}}})
		}
	}
	return issues
}

func applyMediaReplyQuality(in Input, d Decision, v GroundingVerdict) GroundingVerdict {
	for _, issue := range mediaReplyQualityIssues(in, d) {
		v.Verdict = "reject"
		if issue.Index < len(v.Bubbles) {
			v.Bubbles[issue.Index].Verdict = "reject"
			v.Bubbles[issue.Index].Violations = append(v.Bubbles[issue.Index].Violations, issue.Violations...)
		}
	}
	return v
}
