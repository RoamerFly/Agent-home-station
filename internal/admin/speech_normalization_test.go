package admin

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestNormalizeSpeechRejectsRawWeChatSILKWithActionableReason(t *testing.T) {
	for _, raw := range [][]byte{[]byte("#!SILK_V3\x00raw"), append([]byte{2, 0}, []byte("#!SILK_V3\x00raw")...)} {
		if _, err := normalizeSpeech(t.Context(), t.TempDir(), raw); err == nil || !strings.Contains(err.Error(), "normal") {
			t.Fatalf("raw SILK was treated as decoded audio: %v", err)
		}
	}
}

func TestNormalizeSpeechConvertsPlayableWAVToStandardMono16K(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is required for actual audio normalization")
	}
	raw := syntheticReferenceWAV(t, 1) // fixture is valid 8 kHz PCM WAV.
	wav, err := normalizeSpeech(t.Context(), t.TempDir(), raw)
	if err != nil || bytes.Equal(wav, raw) {
		t.Fatal("playable normal voice was not normalized", err)
	}
	rate, seconds, err := namedSpeechWAVInfo(wav)
	if err != nil || rate != 16000 || seconds <= 0 {
		t.Fatalf("invalid normalized output: rate=%d seconds=%f err=%v", rate, seconds, err)
	}
}
