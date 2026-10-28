package admin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/linuxnative"
)

// Explicit local-only diagnostic: generated color frames + the administrator's
// selected synthetic character speech. Calls real configured models; no native
// WeChat connection or real conversation is opened, and nothing is sent.
func TestLiveVideoObservationWithSelectedCharacterSpeech(t *testing.T) {
	root, workspace, audio := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE"), os.Getenv("WECHAT_VIDEO_AUDIO_REFERENCE")
	if root == "" || workspace == "" || audio == "" || os.Getenv("WECHAT_VIDEO_OBSERVATION_LIVE") != "1" {
		t.Skip("explicit multimodal model diagnostic")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal("private model configuration unavailable")
	}
	f := setup(t)
	f.manager.config = c
	f.manager.config.Behavior.ReadVideos = true
	f.manager.speech, err = loadSpeech(filepath.Join(root, "admin"))
	if err != nil || !f.manager.speech.ASREnabled {
		t.Fatal("configured ASR unavailable")
	}
	provider, err := f.manager.makeModel(c)
	if err != nil {
		t.Fatal("configured vision provider unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	video := filepath.Join(f.dir, "synthetic-character-video.mp4")
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", "color=c=blue:s=192x128:r=6:d=4", "-i", audio, "-map", "0:v:0", "-map", "1:a:0", "-t", "4", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", video)
	if cmd.Run() != nil {
		t.Fatal("synthetic video creation failed")
	}
	data, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}
	native := &observationNative{fakeNative: f.native, data: data}
	f.manager.native = native
	msg := linuxnative.Message{ID: "synthetic-live-video", ThreadID: "filehelper", Type: 43}
	text, err := (mediaUnderstanding{manager: f.manager, provider: provider}).Analyze(ctx, msg)
	report := map[string]any{"actual_channels_card": false, "synthetic_local_video": true, "real_model_vision": strings.Contains(text, "[画面观察"), "real_model_asr": strings.Contains(text, "[实际音轨转写"), "wechat_sends": 0, "observations_bounded": strings.Contains(text, "本次读取范围为采样概览"), "local_video_bytes": len(data), "success": err == nil}
	if err != nil {
		report["failure"] = "video_observation_unavailable"
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	if e := os.WriteFile(filepath.Join(workspace, "research/evidence/live-video-multimodal-result.json"), b, 0600); e != nil {
		t.Fatal("cannot save private diagnostic")
	}
	if err != nil || report["real_model_vision"] != true || report["real_model_asr"] != true {
		t.Fatal("real multimodal observation incomplete; see private result flags")
	}
}
