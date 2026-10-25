package admin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Reproduce the historical public MP4 with multiple audio tracks. No network,
// account state or WeChat send is used; run explicitly in the Linux runtime.
func TestPublicVideoRemuxDropsExtraTracksAndPreservesDuration(t *testing.T) {
	if os.Getenv("WECHAT_VIDEO_REGRESSION") != "1" {
		t.Skip("explicit FFmpeg runtime regression")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.f4v")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", "testsrc2=size=160x120:rate=8:duration=2", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-f", "lavfi", "-i", "sine=frequency=880:duration=2", "-map", "0:v:0", "-map", "1:a:0", "-map", "2:a:0", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", "-f", "mp4", source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture encoder: %v %.300s", err, out)
	}
	data, _ := os.ReadFile(source)
	result, err := preparePublicVideo(ctx, dir, data)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "result.mp4")
	if err = os.WriteFile(dest, result, 0600); err != nil {
		t.Fatal(err)
	}
	probe := func(path string) (int, int, float64) {
		out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "stream=codec_type:format=duration", "-of", "json", path).Output()
		if err != nil {
			t.Fatal(err)
		}
		var info struct {
			Streams []struct {
				Type string `json:"codec_type"`
			} `json:"streams"`
			Format struct {
				Duration string `json:"duration"`
			} `json:"format"`
		}
		if json.Unmarshal(out, &info) != nil {
			t.Fatal("bad probe")
		}
		video, audio := 0, 0
		for _, s := range info.Streams {
			if s.Type == "video" {
				video++
			}
			if s.Type == "audio" {
				audio++
			}
		}
		seconds, _ := strconv.ParseFloat(info.Format.Duration, 64)
		return video, audio, seconds
	}
	v, a, before := probe(source)
	if v != 1 || a != 2 {
		t.Fatal("fixture did not reproduce extra audio tracks", v, a)
	}
	v, a, after := probe(dest)
	if v != 1 || a != 1 || after < before-0.05 || after > before+0.05 {
		t.Fatal("remux lost duration or extra tracks survived", v, a, before, after)
	}
	t.Logf("multi-track F4V/MP4 remux: video=%d audio=%d duration=%.3fs (source %.3fs)", v, a, after, before)
}
