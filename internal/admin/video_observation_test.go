package admin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mediaspec"
)

type observationNative struct {
	*fakeNative
	data  []byte
	calls int
}

func TestVideoObservationCoverageSeparatesSampledFramesFromCompleteAudio(t *testing.T) {
	short := videoObservationCoverage(60, []float64{0, 12, 24, 36, 48, 59}, true, 60, true, false)
	if short.Visual != "sampled" || !short.AudioComplete || short.Audio != "transcribed" {
		t.Fatal(short)
	}
	long := videoObservationCoverage(600, []float64{0, 100, 200, 300, 400, 500}, true, 180, true, false)
	if long.AudioComplete || long.NextStart != 180 {
		t.Fatal("partial audio mislabeled complete", long)
	}
	failed := videoObservationCoverage(60, nil, false, 60, false, false)
	if failed.AudioComplete || failed.Audio != "unavailable" {
		t.Fatal(failed)
	}
	noSpeech := videoObservationCoverage(60, nil, false, 60, false, true)
	if !noSpeech.AudioComplete || noSpeech.Audio != "no_recognizable_speech" {
		t.Fatal(noSpeech)
	}
}

func (n *observationNative) IncomingMedia(context.Context, string, int64) ([]byte, string, error) {
	n.calls++
	return n.data, "video/mp4", nil
}

func observationFixture(t *testing.T, audioSeconds string) []byte {
	t.Helper()
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " unavailable")
		}
	}
	out := filepath.Join(t.TempDir(), "synthetic.mp4")
	args := []string{"-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", "color=c=blue:s=96x64:r=6:d=1"}
	if audioSeconds != "" {
		args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:duration="+audioSeconds, "-c:a", "aac")
	}
	args = append(args, "-c:v", "libx264", "-threads", "1", out)
	if raw, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("synthetic encoder failed: %v %.200s", err, raw)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestVideoSamplingDoesNotReuseAFramePastTheVideoTrack(t *testing.T) {
	// Container duration is three seconds; the video track is only one.
	// FFmpeg may exit zero without writing a JPEG for later sample times.
	data := observationFixture(t, "3")
	sample, err := sampleVideo(context.Background(), t.TempDir(), data)
	if err == nil || sample.Seconds < 2.9 || len(sample.Frames) >= 6 || len(sample.Frames) != len(sample.Times) {
		t.Fatal("empty decode reused earlier image", sample.Seconds, len(sample.Frames), err)
	}
	if _, err := sampleVideoFrames(context.Background(), t.TempDir(), data); err == nil {
		t.Fatal("full-frame wrapper accepted incomplete samples")
	}
}

func TestVideoObservationRejectsBeforeDownloadWithoutConfiguredPluginCapability(t *testing.T) {
	video := observationFixture(t, "1")
	f := setup(t)
	native := &observationNative{fakeNative: f.native, data: video}
	f.manager.native = native
	f.manager.config.Behavior.ReadVideos = true
	msg := linuxnative.Message{ID: "synthetic-video", ThreadID: "filehelper", Type: 43}
	if _, err := (mediaUnderstanding{manager: f.manager}).Analyze(context.Background(), msg); err == nil || err.Error() != "media_plugin_migration_required" || native.calls != 0 {
		t.Fatal("unconfigured video path downloaded media or used an old vision fallback", err, native.calls)
	}
}

func TestPluginAudioExtractionPreservesNoRecognizableSpeech(t *testing.T) {
	video := observationFixture(t, "1")
	f := setup(t)
	a := mediaUnderstanding{manager: f.manager}
	text, seconds, err := a.videoAudioRangeTimedWith(context.Background(), video, 0, 1, 60, func(context.Context, []byte) (string, error) {
		return "", &mediaspec.Failure{Code: "ASR_RESPONSE_HAVE_NO_WORDS"}
	})
	if !mediaspec.IsNoSpeech(err) || text != "" || seconds < .9 || seconds > 1.1 {
		t.Fatal("plugin extraction lost no-speech boundary", text, seconds, err)
	}
}

func TestVideoReadPermissionAndCancellationPreventProviderCalls(t *testing.T) {
	f := setup(t)
	native := &observationNative{fakeNative: f.native}
	f.manager.native = native
	f.manager.config.Behavior.ReadVideos = false
	a := mediaUnderstanding{manager: f.manager}
	if _, err := a.Analyze(context.Background(), linuxnative.Message{Type: 43}); err == nil || native.calls != 0 {
		t.Fatal("disabled video read accessed media")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.observeVideo(ctx, linuxnative.Message{}, nil, videoSample{Seconds: 1}, true); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}

func TestVideoWithoutAudioStillKeepsVisualObservation(t *testing.T) {
	video := observationFixture(t, "")
	f := setup(t)
	f.manager.config.Behavior.ReadVideos = true
	f.manager.native = &observationNative{fakeNative: f.native, data: video}
	text, err := (mediaUnderstanding{manager: f.manager}).Analyze(context.Background(), linuxnative.Message{ID: "silent-video", ThreadID: "filehelper", Type: 43})
	if err == nil || err.Error() != "media_plugin_migration_required" || text != "" {
		t.Fatal(text, err)
	}
	if f.manager.native.(*observationNative).calls != 0 {
		t.Fatal("missing plugin downloaded a silent video")
	}
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "video-audio-") || strings.HasPrefix(entry.Name(), "video-analysis-") {
			t.Fatal("temporary decoded media left behind", entry.Name())
		}
	}
}

func TestVideoAudioLimitsActualTrackTo180Seconds(t *testing.T) {
	video := observationFixture(t, "181")
	f := setup(t)
	var submittedBytes int
	a := mediaUnderstanding{manager: f.manager}
	transcribe := func(_ context.Context, wav []byte) (string, error) {
		submittedBytes = len(wav)
		if len(wav) < 44 || string(wav[:4]) != "RIFF" {
			t.Fatal("plugin would receive invalid extracted WAV")
		}
		return "合成180秒音轨转写", nil
	}
	text, seconds, err := a.videoAudioRangeTimedWith(context.Background(), video, 0, 180, 60, transcribe)
	if err != nil || text == "" || seconds < 179.9 || seconds > 180.01 || submittedBytes < 5760000 || submittedBytes > 5761024 {
		t.Fatal("audio exceeded observation coverage", seconds, submittedBytes, err)
	}
	text, seconds, err = a.videoAudioRangeTimedWith(context.Background(), video, 180, 1, 60, transcribe)
	if err != nil || text == "" || seconds < .9 || seconds > 1.1 || submittedBytes > 40000 {
		t.Fatal("could not read audio beyond former180s cutoff", seconds, submittedBytes, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, _, err := a.videoAudioRangeTimedWith(ctx, video, 0, 180, 60, transcribe); err == nil || err.Error() != "video_audio_time_budget_exhausted" {
		t.Fatal("exhausted deadline still attempted extraction", err)
	}
}
