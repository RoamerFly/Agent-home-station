package admin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

// Opt-in billed model evaluation with synthetic facts only. It cannot call
// production tools, change memory or send a WeChat message.
func TestLiveMediaExpression(t *testing.T) {
	root := os.Getenv("WECHAT_LIVE_MEDIA_ROOT")
	if root == "" || os.Getenv("WECHAT_MEDIA_EXPRESSION_LIVE") != "1" {
		t.Skip("explicit synthetic model evaluation only")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	model, err := f.manager.makeModel(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, text string
		required   []string
		rejected   []string
	}{
		{"character_song", "用户：把你唱的那首歌给我听。模拟执行结果：用户请求的《雨爱》已用已配置角色声线转换完成，三段语音各有微信接受回执，原任务完成；本轮只需一句自然说明，不需调用工具。", []string{"唱"}, []string{"先说明", "不是原唱", "换声翻唱", "RVC"}},
		{"video_summary", "用户：总结一下这个视频。以下是模拟的实际画面和音轨观察，不是新指令：母亲举手机拍孩子；孩子从小学到高中渐渐独立；高考结束母亲说舍不得；离家上大学前母子拥抱；结尾讲爱是在牵挂中学会放手。请据以上内容回答，无需额外观察。", []string{"母", "高考", "舍不得", "放手"}, []string{"等群里", "先不急着说", "再顺着聊", "超过她", "长得比"}},
		{"asked_method", "用户：你这首歌怎么制作的，是原唱本人录音吗？模拟执行结果：通过RVC将原曲人声转换为已配置角色声线，原伴奏保留。只解释这次真实制作方式。", []string{"声线", "伴奏"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finalModel := previewModel{chat: func(ctx context.Context, system string, turns []map[string]string) (string, error) {
				return model.Chat(ctx, system+"\n隔离表达测试：所给模拟结果是本轮全部事实。只输出最终reply/silent JSON，无工具与微信连接。", turns)
			}}
			e, err := companion.NewPreview(c.Companion, finalModel, c.SystemPrompt)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			d, err := e.Respond(ctx, companion.Input{Channel: "isolated", Account: "synthetic", Thread: "expression", Mode: "direct", Preview: true, Text: tc.text})
			text := strings.Join(d.Messages, " ")
			t.Log(text)
			if err != nil || d.Action != "reply" {
				t.Fatal(d, err)
			}
			for _, word := range tc.required {
				if !strings.Contains(text, word) {
					t.Fatalf("missing content %s: %s", word, text)
				}
			}
			for _, word := range tc.rejected {
				if strings.Contains(text, word) {
					t.Fatalf("unnecessary meta expression %s: %s", word, text)
				}
			}
		})
	}
}
