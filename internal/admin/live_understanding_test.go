package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/deepseek"
)

// Opt-in diagnostic; ordinary tests never access credentials or bill a model.
func TestLiveUnderstandingDiagnostic(t *testing.T) {
	root := os.Getenv("WECHAT_LIVE_MEDIA_ROOT")
	workspace := os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || workspace == "" {
		t.Skip("explicit local diagnostic only")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal("private configuration unreadable")
	}
	f := setup(t)
	provider, err := f.manager.makeModel(c)
	if err != nil {
		t.Fatal("configured provider unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	im, err := os.ReadFile(filepath.Join(workspace, "internal/admin/static/whale-girl.png"))
	if err != nil {
		t.Fatal(err)
	}
	text, err := provider.Vision(ctx, "描述图中人物外观和主要颜色，用中文回复，不超过150字。", []deepseek.Image{{Data: im}})
	if err != nil {
		t.Fatal("real image understanding failed", err)
	}
	video, err := os.ReadFile(filepath.Join(root, "admin/validation-media/background-video-test.mp4"))
	if err != nil {
		t.Fatal("existing local video fixture missing")
	}
	frames, err := sampleVideoFrames(ctx, f.dir, video)
	if err != nil {
		t.Fatal("video frames failed", err)
	}
	summary, err := provider.Vision(ctx, "这些是按时间排序的视频抽帧。简述可见内容，说明没有听到音轨。", frames)
	if err != nil {
		t.Fatal("real video-frame understanding failed", err)
	}
	report := map[string]any{"configured_model": c.Model, "image_understanding_nonempty": len(text) > 0, "video_frame_count": len(frames), "video_summary_nonempty": len(summary) > 0, "uses_configured_provider_only": true, "wechat_sends": 0, "real_chat_history_read": false, "configuration_changed": false}
	b, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(filepath.Join(workspace, "research/evidence/live-understanding-050.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}
