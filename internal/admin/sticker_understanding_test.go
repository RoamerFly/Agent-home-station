package admin

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os/exec"
	"strings"
	"testing"
	"wechat-bot/internal/linuxnative"
)

type stickerNative struct {
	*fakeNative
	data  []byte
	err   error
	calls int
}

func (n *stickerNative) IncomingMedia(context.Context, string, int64) ([]byte, string, error) {
	n.calls++
	return n.data, "application/octet-stream", n.err
}

func TestStickerAnalysisPreservesUnavailableMessageWithoutInventingContent(t *testing.T) {
	f := setup(t)
	n := &stickerNative{fakeNative: f.native}
	f.manager.native = n
	a := mediaUnderstanding{manager: f.manager}
	m := linuxnative.Message{ID: "sticker", Type: 47, ThreadID: "filehelper"}
	text, err := a.Analyze(context.Background(), m)
	if err == nil || err.Error() != "media_plugin_migration_required" || text != "" || n.calls != 0 {
		t.Fatal(text, err)
	}
	f.manager.config.Behavior.ReadImages = false
	if _, err = a.Analyze(context.Background(), m); err == nil || n.calls != 0 {
		t.Fatal("read permission bypassed")
	}
}
func TestStickerStaticImageUsesConfiguredVisionAndDistinctObservation(t *testing.T) {
	f := setup(t)
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	n := &stickerNative{fakeNative: f.native, data: b.Bytes()}
	f.manager.native = n
	text, err := (mediaUnderstanding{manager: f.manager}).Analyze(context.Background(), linuxnative.Message{ID: "sticker", Type: 47})
	if err == nil || err.Error() != "media_plugin_migration_required" || text != "" || n.calls != 0 {
		t.Fatal(text, err)
	}
}
func TestAnimatedStickerSamplesBoundedBeginning(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	palette := color.Palette{color.Black, color.White}
	a := &gif.GIF{LoopCount: 0}
	for i := 0; i < 4; i++ {
		frame := image.NewPaletted(image.Rect(0, 0, 16, 16), palette)
		if i%2 != 0 {
			for j := range frame.Pix {
				frame.Pix[j] = 1
			}
		}
		a.Image = append(a.Image, frame)
		a.Delay = append(a.Delay, 50)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, a); err != nil {
		t.Fatal(err)
	}
	frames, scope, err := sampleSticker(context.Background(), t.TempDir(), b.Bytes())
	if err != nil || len(frames) != 3 || !strings.Contains(scope, "未验证完整动画") {
		t.Fatal(len(frames), scope, err)
	}
	if _, _, err = sampleSticker(context.Background(), t.TempDir(), []byte("not an image")); err == nil {
		t.Fatal("invalid attachment accepted")
	}
}
