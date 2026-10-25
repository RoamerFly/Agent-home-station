package admin

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
)

func TestMusicBothFormatsStageSameOriginalWithPersonalTitle(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+32000))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint32(16000))
	binary.Write(&b, binary.LittleEndian, uint32(32000))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(32000))
	b.Write(make([]byte, 32000))
	f := setup(t)
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.Companion.Permissions.SendVoice = true
	f.manager.config.Companion.Permissions.SendFile = true
	stager := &fullMusicStager{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = stager
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "both-original"})
	raw, err := p.stageSongOutput(context.Background(), b.Bytes(), "audio/wav", "把晚风装进口袋", "both")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Media    []companion.GeneratedMedia `json:"media"`
		Required []string                   `json:"required_media_ids"`
	}
	if json.Unmarshal([]byte(raw), &result) != nil || len(result.Media) != 2 || len(result.Required) != 2 || result.Media[0].Kind != "voice" || result.Media[1].Kind != "file" {
		t.Fatal("both output missing", raw)
	}
	if result.Media[0].AssetID != result.Media[1].AssetID || result.Media[0].AssetID == "" {
		t.Fatal("voice/file detached from common original")
	}
	if result.Media[1].Title != "大肥鱼原创歌曲-把晚风装进口袋.wav" {
		t.Fatal(result.Media[1].Title)
	}
	assets, err := f.manager.loadAssets()
	if err != nil || len(assets) != 1 || !strings.Contains(assets[0].Filename, "把晚风装进口袋") || assets[0].Sources["voice"] == "" || assets[0].Sources["file"] == "" {
		t.Fatal("original cache naming/formats lost", err, assets)
	}
}

func TestMusicDeliveryIntentAndNames(t *testing.T) {
	for _, tc := range []struct{ text, choice, want string }{
		{"生成后唱给我听并发送文件给我", "both", "both"},
		{"给我唱一首，音频文件也发一下", "both", "both"},
		{"只发文件，不用语音", "both", "file"},
		{"发一首下班听的歌", "file", "voice"},
		{"不用文件，唱给我听", "both", "voice"},
		{"把原音频文件发我", "file", "file"},
	} {
		if got := musicKindFromText(tc.text, tc.choice); got != tc.want {
			t.Errorf("%s: %s want %s", tc.text, got, tc.want)
		}
	}
	if songTitle("鲸鱼娘的原创歌曲") != "" {
		t.Fatal("generic title accepted")
	}
	if got := songTitle("《下班/慢一点》"); got != "下班慢一点" {
		t.Fatal(got)
	}
}
