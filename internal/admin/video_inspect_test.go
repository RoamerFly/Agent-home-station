package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"wechat-bot/internal/bailian"
	"wechat-bot/internal/deepseek"
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

func TestExplicitFramePageAndBatchExceedOldDefaults(t *testing.T) {
	if _, e := exec.LookPath("ffmpeg"); e != nil {
		t.Skip("ffmpeg unavailable")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "pattern.mp4")
	if b, e := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=96x64:r=12:d=8", "-c:v", "libx264", "-threads", "1", path).CombinedOutput(); e != nil {
		t.Fatalf("fixture %v %.100s", e, b)
	}
	in := videoInspectionInput{PageSize: 80, Step: 1, BatchSize: 20, MaxDimension: 96, Tokens: 1200}
	calls, seen := 0, 0
	rows, next, e := inspectFrameRange(context.Background(), path, dir, in, 80, 96, func(_ context.Context, _ string, images []deepseek.Image, tokens int) (string, error) {
		calls++
		seen += len(images)
		if len(images) != 20 || tokens != 1200 {
			t.Fatal("explicit options overridden")
		}
		return "synthetic analysis", nil
	})
	if e != nil || len(rows) != 4 || calls != 4 || seen != 80 || next != 80 {
		t.Fatal("page or batch clipped", len(rows), calls, seen, next, e)
	}
	remaining, _ := os.ReadDir(dir)
	if len(remaining) != 1 {
		t.Fatal("decoded batches were not cleaned incrementally", len(remaining))
	}
}

func TestExplicitAudioRangeAndChunkAreNotClampedTo180Seconds(t *testing.T) {
	data := observationFixture(t, "240")
	f, p := trustedAgent(t)
	defer p.hub.Close()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{"output": map[string]string{"text": "synthetic transcription"}})
	}))
	defer server.Close()
	f.manager.speech = bailian.Defaults()
	f.manager.speech.ASREnabled = true
	f.manager.speech.Endpoint, f.manager.speech.Key = server.URL, "synthetic-test-key"
	out := map[string]any{}
	e := p.inspectAudioRange(context.Background(), data, videoInspectionInput{AudioLength: 240, AudioChunk: 200}, 240, 300, out)
	if e != nil || calls != 2 || out["end_seconds"].(float64) < 239.9 || !out["requested_range_complete"].(bool) || out["video_audio_complete"].(bool) || out["effective_audio_chunk_seconds"] != float64(200) {
		t.Fatal("audio request was shortened", calls, out, e)
	}
	out = map[string]any{}
	e = p.inspectAudioRange(context.Background(), data, videoInspectionInput{AudioLength: 240, AudioChunk: 600}, 240, 300, out)
	if e != nil || out["end_seconds"].(float64) < 239.9 || out["audio_chunk_adjustment_reason"] == nil {
		t.Fatal("transport split lost requested range or explanation", out, e)
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
