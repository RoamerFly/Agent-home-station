package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/linuxnative"
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

// Explicit diagnostic of one received attachment. Read and understand only;
// no live conversation memory is changed and no WeChat reply is submitted.
func TestLiveReceivedImageObservation(t *testing.T) {
	root, selection, output := os.Getenv("WECHAT_IMAGE_LIVE_ROOT"), os.Getenv("WECHAT_IMAGE_LIVE_SELECTION"), os.Getenv("WECHAT_IMAGE_LIVE_OUTPUT")
	if root == "" || selection == "" || output == "" {
		t.Skip("explicit received image diagnostic")
	}
	var selected struct{ Account, ID string }
	b, e := os.ReadFile(selection)
	if e != nil || json.Unmarshal(b, &selected) != nil {
		t.Fatal("selection unavailable")
	}
	c, e := loadConfig(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal("config unavailable")
	}
	n, e := linuxnative.NewAdmin(linuxnative.Config{URL: "http://127.0.0.1:6174", TokenFile: filepath.Join(root, "api-token"), Account: selected.Account})
	if e != nil {
		t.Fatal("native unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	m, e := n.Message(ctx, selected.ID)
	if e != nil || m.Type != 3 || !permittedRecipient(c, m.ThreadID) {
		t.Fatal("image unavailable or scope denied")
	}
	f := setup(t)
	f.manager.config = c
	f.manager.native = n
	provider, e := f.manager.makeModel(c)
	if e != nil {
		t.Fatal("vision provider unavailable")
	}
	observation, e := (mediaUnderstanding{manager: f.manager}).Analyze(ctx, m)
	if e != nil {
		t.Fatal("actual image reading failed", e)
	}
	if !strings.Contains(observation, "[收到图片的视觉观察]") {
		t.Fatal("actual observation missing")
	}
	engine, e := companion.New(c.Companion, f.manager.memory, provider, c.SystemPrompt)
	if e != nil {
		t.Fatal(e)
	}
	review, e := engine.ReviewGeneration(ctx, companion.Input{Channel: "wechat", Account: m.AccountID, Thread: m.ThreadID, Sender: m.SenderID, TraceID: m.ID, IsGroup: true, Mode: "social", Text: "[收到图片]\n" + observation}, []string{"图片已完成真实观察，正在决定如何回应；后续文字可能是图片说明。"}, []companion.ConversationEvent{{MessageID: "synthetic-caption", Sender: m.SenderID, Text: "早餐", NewRequest: true}})
	if e != nil || review.Action != "continue" && review.Action != "delay" {
		t.Fatal("model discarded observed picture for its caption", review.Action, review.Reason, e)
	}
	b, _ = json.MarshalIndent(map[string]any{"observation": observation, "wechat_sends": 0, "actual_received_image": true, "caption_review_action": review.Action}, "", "  ")
	if e = os.WriteFile(output, b, 0600); e != nil {
		t.Fatal(e)
	}
	t.Log("Actual received image downloaded and understood; no WeChat sends")
}
