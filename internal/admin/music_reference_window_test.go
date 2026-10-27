package admin

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"testing"
	"wechat-bot/internal/companion"
)

func TestLiveReadOnlyReferenceClip(t *testing.T) {
	path := os.Getenv("WECHAT_REFERENCE_CLIP_INPUT")
	if path == "" {
		t.Skip("opt-in existing local audio; no generation or delivery")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	clip, seconds, err := prepareReferenceClip(context.Background(), t.TempDir(), data, 70, 60)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("existing recording bytes=%d selected excerpt bytes=%d prepared seconds=%.3f; no generation or WeChat sends", len(data), len(clip), seconds)
}

func syntheticReferenceWAV(t *testing.T, seconds int) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg required for real reference clip preparation")
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+seconds*16000))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(8000), uint32(16000), uint16(2), uint16(16)} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(seconds*16000))
	for i := 0; i < seconds*8000; i++ {
		value := int16(0)
		if i >= 70*8000 {
			value = int16(8000 * math.Sin(2*math.Pi*440*float64(i)/8000))
		}
		binary.Write(&b, binary.LittleEndian, value)
	}
	return b.Bytes()
}

func TestReferenceWindowFollowsSingingEvidenceAndPreservesExplicitZero(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper"})
	a := mediaAsset{Hash: "synthetic-audio"}
	e, _ := p.saveLyricEvidence(a, "合成歌词", "asr_candidate", 70, 40)
	start, seconds, err := p.referenceWindow(a, []string{e.ID}, 60, nil, nil)
	if err != nil || start != 70 || seconds != 40 {
		t.Fatal(start, seconds, err)
	}
	zero, length := 0.0, 60.0
	start, seconds, err = p.referenceWindow(a, []string{e.ID}, 60, &zero, &length)
	if err != nil || start != 0 || seconds != 60 {
		t.Fatal("explicit range changed", start, seconds, err)
	}
}

func TestReferenceClipUploadsOnlyChosenRangeAndKeepsOriginal(t *testing.T) {
	source := syntheticReferenceWAV(t, 140)
	before := append([]byte{}, source...)
	clip, seconds, err := prepareReferenceClip(context.Background(), t.TempDir(), source, 70, 10)
	if err != nil || seconds < 9.99 || seconds > 10.01 || len(clip) < 8 || string(clip[4:8]) != "ftyp" || len(clip) >= len(source) || !bytes.Equal(source, before) {
		t.Fatal("clip was not prepared independently", len(clip), seconds, err)
	}
	if _, _, err = prepareReferenceClip(context.Background(), t.TempDir(), source, 135, 10); err == nil {
		t.Fatal("explicit out-of-range selection silently shortened")
	}
}
