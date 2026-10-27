package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"wechat-bot/internal/mcptools"
)

func TestVideoInspectionFailuresExplainNextStepWithoutRawSecrets(t *testing.T) {
	if text := mcptools.FailureDescription(videoInspectionError(errors.New("video_inspect_probe_failed"))); !strings.Contains(text, "ffprobe") {
		t.Fatal(text)
	}
	if text := mcptools.FailureDescription(videoInspectionError(errors.New("private-path-and-token"))); strings.Contains(text, "private-path") {
		t.Fatal("raw failure leaked")
	}
}

func inspectionFixture(t *testing.T) []byte {
	t.Helper()
	if _, e := exec.LookPath("ffmpeg"); e != nil {
		t.Skip("ffmpeg unavailable")
	}
	path := filepath.Join(t.TempDir(), "synthetic.mp4")
	if b, e := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", "testsrc2=s=96x64:r=12:d=2", "-c:v", "libx264", "-threads", "1", path).CombinedOutput(); e != nil {
		t.Fatalf("synthetic encoding %v %.200s", e, b)
	}
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return data
}

func TestVideoInspectionDecodesEveryFrameAcrossPages(t *testing.T) {
	data := inspectionFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "input.mp4")
	os.WriteFile(path, data, 0600)
	ctx := context.Background()
	meta, e := probeInspectionVideo(ctx, path)
	if e != nil || meta.Frames != 24 {
		t.Fatal(meta, e)
	}
	last := -1.0
	total := 0
	for _, page := range []struct{ start, count int }{{0, 13}, {13, 11}} {
		out := t.TempDir()
		images, times, e := extractInspectionFrames(ctx, path, out, page.start, page.count, 1)
		if e != nil || len(images) != page.count || len(times) != page.count {
			t.Fatal("frame page incomplete", len(images), e)
		}
		for _, at := range times {
			if at <= last {
				t.Fatal("frame timestamp repeated", at, last)
			}
			last = at
			total++
		}
	}
	if total != meta.Frames {
		t.Fatal("frames skipped", total)
	}
	for _, step := range []int{1, 7, int(^uint(0) >> 1)} {
		if n := remainingInspectionFrames(24, 0, step); n < 1 || n > 24 {
			t.Fatal("overflow", n)
		}
	}
}

func TestVideoInspectionUsesScopedCacheAndAdvertisesContinuation(t *testing.T) {
	f, p := trustedAgent(t)
	defer p.hub.Close()
	f.manager.config.Behavior.ReadVideos = true
	f.manager.config.Companion.Permissions.Remember = true
	a, e := f.manager.rememberAssetFormats(p.trace, assetOriginal{Data: inspectionFixture(t), MIME: "video/mp4", Filename: "synthetic.mp4"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	raw := json.RawMessage(fmt.Sprintf(`{"action":"info","asset_id":%q}`, a.ID))
	value, e := p.Call(context.Background(), "video_inspect", raw)
	var out struct{ Metadata videoMetadata }
	if e != nil || json.Unmarshal([]byte(value), &out) != nil || out.Metadata.Frames != 24 {
		t.Fatal(value, e)
	}
	p.trace.Thread = "filehelper"
	if _, e = p.Call(context.Background(), "video_inspect", raw); e == nil {
		t.Fatal("cross-conversation video exposed")
	}
	f.manager.config.Behavior.ReadVideos = false
	if p.enabled("video_inspect") {
		t.Fatal("closed video permission ignored")
	}
}
