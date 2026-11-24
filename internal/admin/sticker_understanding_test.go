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
	if err == nil || !strings.Contains(text, "本次画面读取失败") || n.calls != 1 {
		t.Fatal(text, err)
	}
	f.manager.config.Behavior.ReadImages = false
	if _, err = a.Analyze(context.Background(), m); err == nil || n.calls != 1 {
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
	if err == nil || err.Error() != "media_plugin_migration_required" || !strings.Contains(text, "本次画面读取失败") || n.calls != 1 {
		t.Fatal(text, err)
	}
}

func TestStickerHashReusesObservedContentWithoutVisionCall(t *testing.T) {
	f := setup(t)
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	n := &stickerNative{fakeNative: f.native, data: b.Bytes()}
	f.manager.native = n
	_, digest, err := f.manager.storeMessageMediaBytes("stickers", ".png", b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.manager.saveMessageObservation("stickers", digest, "单张画面", "一只挥手的小猫"); err != nil {
		t.Fatal(err)
	}
	text, err := (mediaUnderstanding{manager: f.manager}).Analyze(context.Background(), linuxnative.Message{ID: "same-sticker-new-message", Type: 47})
	if err != nil || !strings.Contains(text, "一只挥手的小猫") || n.calls != 1 {
		t.Fatal(text, err, n.calls)
	}
}
func TestAnimatedStickerSamplesAcrossCompleteCycle(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
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
	if err != nil || len(frames) < 4 || !strings.Contains(scope, "完整动画约2.0秒") {
		t.Fatal(len(frames), scope, err)
	}
	if bytes.Equal(frames[0].Data, frames[len(frames)-1].Data) {
		t.Fatal("late animation state was not sampled")
	}
	if _, _, err = sampleSticker(context.Background(), t.TempDir(), []byte("not an image")); err == nil {
		t.Fatal("invalid attachment accepted")
	}
}

func TestAnimatedWebPChunkDetection(t *testing.T) {
	animated := append([]byte("RIFF\x10\x00\x00\x00WEBPANIM"), 0, 0, 0, 0)
	static := append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), 0, 0, 0, 0)
	if !animatedWebP(animated) || animatedWebP(static) {
		t.Fatal("WebP animation chunk misclassified")
	}
}

func TestAnimatedPNGChunkDetection(t *testing.T) {
	animated := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x00acTL"), 0, 0, 0, 0)
	static := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x00IHDR"), 0, 0, 0, 0)
	if !animatedPNG(animated) || animatedPNG(static) {
		t.Fatal("PNG animation chunk misclassified")
	}
}
