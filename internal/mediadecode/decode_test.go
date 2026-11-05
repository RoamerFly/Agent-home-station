package mediadecode

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	path := filepath.Join(t.TempDir(), "synthetic.mp4")
	if output, err := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", "testsrc2=s=96x64:r=12:d=2", "-c:v", "libx264", "-threads", "1", path).CombinedOutput(); err != nil {
		t.Fatalf("fixture encode: %v %.200s", err, output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDurationValidation(t *testing.T) {
	for _, raw := range []string{"", "0", "-1", "NaN", "+Inf"} {
		if _, err := ParseDuration(raw); err == nil {
			t.Fatalf("invalid duration accepted: %q", raw)
		}
	}
	seconds, err := ParseDuration("1.25")
	if err != nil || math.Abs(seconds-1.25) > 0.001 || FormatSeconds(seconds) != "1.250" {
		t.Fatalf("seconds=%v err=%v", seconds, err)
	}
}

func TestSampleProbeAndFrameExtractionPreserveEvidence(t *testing.T) {
	data := fixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	sample, err := Sample(ctx, dir, data)
	if err != nil || len(sample.Frames) != 6 || len(sample.Times) != 6 || sample.Seconds <= 0 {
		t.Fatalf("sample=%+v err=%v", sample, err)
	}
	for _, frame := range sample.Frames {
		if frame.MIME != "image/jpeg" || len(frame.Data) == 0 {
			t.Fatal("sample frame is not evidence")
		}
	}
	path := filepath.Join(dir, "input.mp4")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if wav, seconds, err := ExtractAudioRange(ctx, path, filepath.Join(dir, "audio.wav"), 0, 1); !errors.Is(err, ErrNoAudioTrack) || len(wav) != 0 || seconds != 0 {
		t.Fatalf("video without an audio stream: %d bytes, %v seconds, %v", len(wav), seconds, err)
	}
	meta, err := Probe(ctx, path)
	if err != nil || meta.Frames != 24 || meta.Width != 96 || meta.Height != 64 {
		t.Fatalf("metadata=%+v err=%v", meta, err)
	}
	frames, times, err := ExtractFramesSized(ctx, path, t.TempDir(), 0, 24, 1, 96)
	if err != nil || len(frames) != 24 || len(times) != 24 {
		t.Fatalf("frames=%d times=%d err=%v", len(frames), len(times), err)
	}
	for i := 1; i < len(times); i++ {
		if times[i] <= times[i-1] {
			t.Fatalf("timestamps are not consecutive: %v then %v", times[i-1], times[i])
		}
	}
}
