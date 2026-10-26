package companion

import "strings"

// Current model input remains complete. Persisted turns split their existing
// byte budget so a long frame description cannot erase the actual transcript.
func boundedInputMemory(text string) string {
	if len(text) <= 2048 || !strings.Contains(text, "[微信视频号实际内容观察") && !strings.Contains(text, "[微信视频实际内容观察") {
		return boundedText(text, 2048)
	}
	visual, audio := strings.Index(text, "[画面观察"), strings.Index(text, "[实际音轨转写")
	if audio < 0 {
		audio = strings.Index(text, "[音轨转写未")
	}
	if visual < 0 || audio <= visual {
		return boundedText(text, 2048)
	}
	header := strings.Index(text, "[微信视频号实际内容观察")
	if header < 0 {
		header = strings.Index(text, "[微信视频实际内容观察")
	}
	return boundedText(text[:header], 260) + "\n" + boundedText(text[header:visual], 140) + "\n" + boundedText(text[visual:audio], 720) + "\n" + boundedText(text[audio:], 900)
}
