package admin

import (
	"strings"
	"testing"
)

func TestLyricAlignmentFindsCorrespondingPassageAndMinorASRError(t *testing.T) {
	asr := "晚风吹过窗台，我听见花开的声因，回家的路很长，你一直在我身旁"
	candidate := "[Intro]\n这是前面的一段歌词，不属于当前窗口\n[Verse]\n晚风吹过窗台，我听见花开的声音，回家的路很长，你一直在我身旁\n[Chorus]\n这是另外一段副歌，不应该替换进去"
	corrected, edits, score := alignLyricWindow(asr, candidate)
	if edits != 1 || score < .88 || !strings.Contains(corrected, "声音") || strings.Contains(corrected, "前面的") || strings.Contains(corrected, "副歌") {
		t.Fatal(corrected, edits, score)
	}
	if !pageContainsLyrics("Source: https://example.org\n晚风吹过窗台 我听见花开的声音", "晚风吹过窗台，\n我听见花开的声音") {
		t.Fatal("web formatting rejected")
	}
	if pageContainsLyrics("网上只有歌名，没有歌词", "模型自己编的歌词") {
		t.Fatal("invented web lyrics accepted")
	}
}
func TestLyricAlignmentRejectsOtherVersionAndShortAmbiguousPhrase(t *testing.T) {
	_, _, score := alignLyricWindow("晚风吹过窗台，我听见花开的声音，回家的路很长，你一直在我身旁", "牛肉面汤里的思念，我把往事留在昨天，热气模糊双眼，忘记所有从前")
	if score >= .88 {
		t.Fatal("different version matched", score)
	}
	text, _, score := alignLyricWindow("生日快乐", "祝你生日快乐祝你生日快乐")
	if text != "" || score != 0 {
		t.Fatal("short phrase certified", text, score)
	}
}
