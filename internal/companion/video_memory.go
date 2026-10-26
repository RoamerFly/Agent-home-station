package companion

import "strings"

// Current model input remains complete. Persisted turns split their existing
// byte budget so a long frame description cannot erase the actual transcript.
func boundedInputMemory(text string) string {
	if len(text) <= 2048 || !strings.Contains(text, "[微信视频号实际内容观察") {
		return boundedText(text, 2048)
	}
	visual, audio := strings.Index(text, "[画面观察"), strings.Index(text, "[实际音轨转写")
	if audio < 0 {
		audio = strings.Index(text, "[音轨转写未")
	}
	if visual < 0 || audio <= visual {
		return boundedText(text, 2048)
	}
	return boundedText(text[:visual], 380) + "\n" + boundedText(text[visual:audio], 820) + "\n" + boundedText(text[audio:], 820)
}
