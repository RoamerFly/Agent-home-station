package companion

import (
	"strings"
	"time"
)

// Task continuations are runtime evidence, not new statements by the requester.
// Their authoritative originals have already entered through the inbound path.
func derivedObservationInput(in Input) bool {
	if in.BackgroundResult || in.Mode == "proactive" || in.Surf || in.ReportOnly {
		return true
	}
	return (in.TaskID != "" || len(in.UnreadMessages) > 0) && !freshNativeObservation(in)
}

// A task association can accompany a genuinely new native user supplement.
// Resumed task-run traces instead refer back to an older native message ID.
func freshNativeObservation(in Input) bool {
	src := in.Source
	if in.OriginalText == nil || src == nil || in.TraceID == "" || src.MessageID != in.TraceID || src.ServerID == "" || src.OccurredAt == "" || in.MessageTime == "" || src.Sender != "" && src.Sender != in.Sender {
		return false
	}
	occurred, err := time.Parse(time.RFC3339Nano, src.OccurredAt)
	if err != nil {
		return false
	}
	messageAt, err := time.Parse(time.RFC3339Nano, in.MessageTime)
	return err == nil && occurred.Equal(messageAt)
}

func observedUserTextState(in Input) string {
	if in.OriginalText != nil {
		return "verified_original"
	}
	if derivedObservationInput(in) || len(in.IncomingMedia) > 0 || in.Source != nil && in.Source.MessageType != 0 && in.Source.MessageType != 1 || UserRequestText(in.Text) != in.Text {
		return "unknown"
	}
	// Old adapters omitted the plain-text type. Accept ordinary unwrapped text,
	// while leaving synthetic media placeholders unknown even without metadata.
	text := strings.TrimSpace(in.Text)
	for _, marker := range []string{"[图片]", "[视频]", "[语音]", "[文件]", "[image]", "[video]", "[voice]", "[audio]", "[file]"} {
		if strings.HasPrefix(strings.ToLower(text), marker) {
			return "unknown"
		}
	}
	return "verified_original"
}
