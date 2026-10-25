package admin

import (
	"testing"
	"wechat-bot/internal/companion"
)

func TestMusicRequestedDurationAndDefaultVoiceRespectUserRequest(t *testing.T) {
	for text, want := range map[string]int{"重新唱一首一分钟左右的歌": 60, "做一首60秒的歌": 60, "半分钟": 30, "一分半": 90, "三分钟": 180, "忧郁风格": 0} {
		if got := musicRequestedSeconds(text); got != want {
			t.Fatal(text, got, want)
		}
	}
	f := setup(t)
	f.manager.config.Companion.Permissions.SendVoice = true
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Text: "重新唱一首一分钟左右的歌"})
	if p.musicDeliveryKind("file") != "voice" {
		t.Fatal("model selected unsolicited file")
	}
	p.trace.Text = "把刚才的原音频文件发我"
	if p.musicDeliveryKind("file") != "file" {
		t.Fatal("explicit file request lost")
	}
	p.trace.Text = "不要文件，发语音"
	if p.musicDeliveryKind("file") != "voice" {
		t.Fatal("file refusal ignored")
	}
	if defaultAudioPreview("给我发一首歌") != 0 {
		t.Fatal("full song became a preview")
	}
}
