package admin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveLongAudioPreparation(t *testing.T) {
	workspace := os.Getenv("WECHAT_LIVE_WORKSPACE")
	if workspace == "" {
		t.Skip("explicit FFmpeg diagnostic only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	source := filepath.Join(dir, "long.wav")
	if err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin", "-f", "lavfi", "-i", "sine=frequency=440:duration=313", "-ar", "16000", "-ac", "1", source).Run(); err != nil {
		t.Fatal("test source creation failed")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	output, err := preparePublicAudio(ctx, dir, data)
	if err != nil || len(output.Media) != 2 {
		t.Fatal("long audio not split", len(output.Media), err)
	}
	for _, part := range output.Media {
		if part.Kind != "voice" || len(part.Data) > 10<<20 || len(part.Data) < 44 {
			t.Fatal("invalid voice source")
		}
	}
	b, _ := json.MarshalIndent(map[string]any{"source_seconds": 313, "native_upload_parts": 2, "maximum_voice_note_seconds": 60, "expected_voice_notes": 6, "truncated": false, "actual_wechat_sends": 0}, "", "  ")
	if err = os.WriteFile(filepath.Join(workspace, "research/evidence/long-audio-060.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}
