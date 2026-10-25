package admin

import (
	"testing"
	"wechat-bot/internal/companion"
)

func TestMusicFormatHonorsModelDecisionAndPermissions(t *testing.T) {
	f := setup(t)
	f.manager.config.Companion.Permissions.SendVoice = true
	f.manager.config.Companion.Permissions.SendFile = true
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Text: "唱完把下载版也给我"})
	for _, kind := range []string{"voice", "file", "both"} {
		if p.musicDeliveryKind(kind) != kind {
			t.Fatal("semantic choice was overwritten", kind)
		}
	}
	if p.musicDeliveryKind("") != "voice" {
		t.Fatal("voice default missing")
	}
	f.manager.config.Companion.Permissions.SendFile = false
	if p.musicKindPermitted("both") {
		t.Fatal("both bypassed file permission")
	}
	if p.musicDeliveryKind("both") != "both" {
		t.Fatal("permission failure silently dropped requested file")
	}
	if defaultAudioPreview("给我发一首歌") != 0 {
		t.Fatal("full song became preview")
	}
}
