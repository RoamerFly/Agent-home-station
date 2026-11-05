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

func TestLiveNaturalReplyStyle(t *testing.T) {
	root := os.Getenv("WECHAT_LIVE_MEDIA_ROOT")
	if root == "" || os.Getenv("WECHAT_REPLY_STYLE_LIVE") != "1" {
		t.Skip("explicit billed synthetic model diagnostic; no WeChat sends")
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
	cases := []string{
		"你刚刚说‘暂时不能把有回执当成可播放’，能直接说清怎么回事吗？【模拟已核实的执行结果：音频已送到目标群，手机播放尚未测试；没有安排重发。】",
		"视频里说了什么？【模拟观察：标题是城市夜景，视频画面读取失败，音轨识别没有返回文字，没有重读任务。】",
		"查完了吗，修好了没有？【模拟执行结果：已定位为目标地址错误，代码尚未修改，没有安排后台修复任务。】",
		"别的歌怎么又发了？【模拟结果：旧冲浪音乐的剩余发送已取消，正常聊天已恢复，用户请求歌曲的三段均有接受回执。】",
	}
	for _, input := range cases {
		// The diagnostic has no execution tools. Ask for a terminal decision
		// from synthetic completed observations, rather than a search attempt.
		finalModel := previewModel{chat: func(ctx context.Context, system string, turns []map[string]string) (string, error) {
			return model.Chat(ctx, system+"\n本轮是隔离的表达评估，所给模拟执行结果为全部已知事实。只输出最终reply/silent JSON，不调用工具，不安排操作；直接回答当前问题。", turns)
		}}
		e, err := companion.NewPreview(c.Companion, finalModel, c.SystemPrompt)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		d, err := e.Respond(ctx, companion.Input{Channel: "isolated", Account: "synthetic", Thread: "style-evaluation", Mode: "direct", Preview: true, Directed: true, Text: input})
		cancel()
		text := strings.Join(d.Messages, " ")
		t.Logf("synthetic input=%s reply=%s", input, text)
		if err != nil || d.Action != "reply" || text == "" {
			t.Fatal(d, err)
		}
		for _, bad := range []string{"不能把", "不能声称", "不能说", "只能说", "不等于", "不能保证", "为了避免误导"} {
			if strings.Contains(text, bad) {
				t.Fatalf("defensive phrasing %q in %s", bad, text)
			}
		}
	}
}
