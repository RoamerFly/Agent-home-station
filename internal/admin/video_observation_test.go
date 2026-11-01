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
	"wechat-bot/internal/bailian"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/linuxnative"
)

type observationVision func(context.Context, string, []deepseek.Image) (string, error)

func (fn observationVision) Vision(ctx context.Context, prompt string, images []deepseek.Image) (string, error) {
	return fn(ctx, prompt, images)
}

type observationNative struct {
	*fakeNative
	data  []byte
	calls int
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

func TestVideoObservationModalitiesAreIndependentAndScoped(t *testing.T) {
	video := observationFixture(t, "1")
	for _, tc := range []struct {
		name, visual string
		wantError    bool
	}{
		{"visual", "合成蓝色画面", false},
		{"visual-fail", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			native := &observationNative{fakeNative: f.native, data: video}
			f.manager.native = native
			f.manager.config.Behavior.ReadVideos = true
			visionCalls := 0
			vision := observationVision(func(ctx context.Context, prompt string, images []deepseek.Image) (string, error) {
				visionCalls++
				if len(images) < 1 || len(images) > 6 || !strings.Contains(prompt, "采样时刻") {
					t.Error("missing temporal evidence", len(images))
				}
				if tc.visual == "" {
					return "", errors.New("synthetic-provider-failure-private-detail")
				}
				return tc.visual, nil
			})
			msg := linuxnative.Message{ID: "synthetic-video", ThreadID: "filehelper", Type: 43}
			text, err := (mediaUnderstanding{manager: f.manager, provider: vision}).Analyze(context.Background(), msg)
			if (err != nil) != tc.wantError || native.calls != 1 || visionCalls != 1 {
				t.Fatal(text, err, native.calls, visionCalls)
			}
			if tc.wantError {
				return
			}
			if !strings.Contains(text, "本次读取范围为采样概览") || strings.Contains(text, "private-detail") {
				t.Fatal("observation boundary or error redaction lost", text)
			}
			if tc.visual != "" && !strings.Contains(text, tc.visual) {
				t.Fatal("successful evidence discarded", text)
			}
			if !strings.Contains(text, "媒体理解插件未安装或未启用语音识别") {
				t.Fatal("missing plugin state was not disclosed", text)
			}
			if tc.visual == "" && !strings.Contains(text, "画面理解未完成") {
				t.Fatal("failed modality was presented as observed", text)
			}
		})
	}
}

func TestPluginAudioExtractionPreservesNoRecognizableSpeech(t *testing.T) {
	video := observationFixture(t, "1")
	f := setup(t)
	a := mediaUnderstanding{manager: f.manager}
	text, seconds, err := a.videoAudioRangeTimedWith(context.Background(), video, 0, 1, 60, func(context.Context, []byte) (string, error) {
		return "", &bailian.Failure{Code: "ASR_RESPONSE_HAVE_NO_WORDS"}
	})
	if !bailian.IsNoSpeech(err) || text != "" || seconds < .9 || seconds > 1.1 {
		t.Fatal("plugin extraction lost no-speech boundary", text, seconds, err)
	}
}

func TestVideoReadPermissionAndCancellationPreventProviderCalls(t *testing.T) {
	f := setup(t)
	native := &observationNative{fakeNative: f.native}
	f.manager.native = native
	f.manager.config.Behavior.ReadVideos = false
	vision := observationVision(func(context.Context, string, []deepseek.Image) (string, error) {
		t.Fatal("provider called without permission or after cancellation")
		return "", nil
	})
	a := mediaUnderstanding{manager: f.manager, provider: vision}
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
	vision := observationVision(func(context.Context, string, []deepseek.Image) (string, error) {
		return "合成无声蓝色画面", nil
	})
	text, err := (mediaUnderstanding{manager: f.manager, provider: vision}).Analyze(context.Background(), linuxnative.Message{ID: "silent-video", ThreadID: "filehelper", Type: 43})
	if err != nil || !strings.Contains(text, "合成无声蓝色画面") || !strings.Contains(text, "媒体理解插件未安装或未启用语音识别") || strings.Contains(text, "[实际音轨转写") {
		t.Fatal(text, err)
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
