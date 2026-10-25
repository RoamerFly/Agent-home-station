package companion

import (
	"context"
	"strings"
	"unicode/utf8"
)

type DecisionDiagnostic struct {
	Code    string
	Attempt int
}
type decisionObserverKey struct{}

func WithDecisionObserver(ctx context.Context, fn func(DecisionDiagnostic)) context.Context {
	return context.WithValue(ctx, decisionObserverKey{}, fn)
}
func observeDecision(ctx context.Context, code string, attempt int) {
	if fn, ok := ctx.Value(decisionObserverKey{}).(func(DecisionDiagnostic)); ok {
		fn(DecisionDiagnostic{code, attempt})
	}
}

func decisionShape(d Decision, maxBubbles int) string {
	switch d.Action {
	case "tool":
		return ""
	case "silent", "wait", "blocked":
		if len(d.Messages) > 0 || len(d.MediaIDs) > 0 || d.ImageID != "" {
			return "decision_silence_has_output"
		}
		return ""
	case "reply":
		if len(d.Messages) == 0 && d.ImageID == "" && len(d.MediaIDs) == 0 {
			return "decision_reply_empty"
		}
		if len(d.Messages) > maxBubbles {
			return "decision_bubbles_exceeded"
		}
		if len(d.MediaIDs) > 3 {
			return "decision_media_count_exceeded"
		}
		for _, s := range d.Messages {
			if strings.TrimSpace(s) == "" || len(s) > 3000 || !utf8.ValidString(s) {
				return "decision_message_invalid"
			}
		}
		return ""
	default:
		return "decision_action_invalid"
	}
}
func decisionProtocolText(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "dsml") || strings.Contains(lower, "<tool") || strings.Contains(lower, "tool_calls") || strings.Contains(lower, "function_call")
}
func claimsUnobservedVideo(messages []string) bool {
	text := strings.Join(messages, " ")
	for _, phrase := range []string{"我看了", "我看过", "我看完", "刚看完", "看完整个人", "视频里", "片中", "画面中", "里面有只", "里面的小"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}
