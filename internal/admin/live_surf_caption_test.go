package admin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

// Real model review, synthetic media evidence; no WeChat sender or real chats.
func TestLiveSurfAcceptedVideoRetainsCaptionUnlessStopped(t *testing.T) {
	root := os.Getenv("WECHAT_LIVE_MEDIA_ROOT")
	if root == "" || os.Getenv("WECHAT_SURF_CAPTION_LIVE") != "1" {
		t.Skip("explicit synthetic caption model review")
	}
	live, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	model, err := f.manager.makeModel(live)
	if err != nil {
		t.Fatal(err)
	}
	c := live.Companion
	c.Enabled = true
	e, err := companion.New(c, f.manager.memory, model, live.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "synthetic@chatroom", TraceID: "surf-synthetic", Mode: "proactive", Surf: true, IsGroup: true, Text: "自主分享一个轻松短视频。"}
	media := []companion.DeliveredAttachment{{Kind: "video", Title: "合成测试：猫咪钻纸箱", State: "accepted"}}
	for _, stop := range []bool{false, true} {
		text := "今天出太阳了"
		if stop {
			text = "别再发这段视频的文字了，停一下"
		}
		d, err := e.ReviewMediaCaption(ctx, in, nil, []string{"猫咪钻纸箱的短片，忙里看个小乐子～"}, []companion.ConversationEvent{{MessageID: "synthetic-update", Sender: "person", Text: text}}, media)
		if err != nil {
			t.Fatal(err)
		}
		if !stop && d.Action != "continue" && d.Action != "replace" || stop && d.Action != "cancel" {
			t.Fatal("caption semantics not followed", stop, d.Action, d.Reason)
		}
		t.Logf("real_model=true stop_requested=%t action=%s wechat_sends=0", stop, d.Action)
	}
}
