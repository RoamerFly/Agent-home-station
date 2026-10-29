package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestLiveSemanticPlanAndNewContextReview(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || workspace == "" || os.Getenv("WECHAT_CONVERSATION_LIVE") != "1" {
		t.Skip("explicit isolated conversation model diagnostic")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	provider, err := f.manager.makeModel(c)
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.Companion
	cfg.QuietStart = "00:00"
	cfg.QuietEnd = "00:00"
	cfg.MaxBubbles = 3
	cfg.Conversation = companion.ConversationDefaults()
	store, err := companion.NewStore(t.TempDir())
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	engine, err := companion.New(cfg, store, provider, c.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	in := companion.Input{Channel: "isolated", Account: "test", Thread: "semantic-plan-probe", Mode: "direct", Directed: true, TraceID: "isolated-plan", Text: "我总把写周报拖到最后，能用两条短消息给我两个小建议吗？每条表达一个完整意思，不要自我介绍。"}
	engine, err = engine.Fork()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := engine.Respond(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	review, err := engine.ReviewPending(ctx, in, []string{"先把本周做过的事情记下来。"}, []string{"然后再展开每一项。", "我还可以继续举几个例子。"}, []companion.ConversationEvent{{MessageID: "new", Sender: "test-person", Text: "先别继续讲了，我现在要去开会，不用回复。", At: time.Now().UTC().Format(time.RFC3339), NewRequest: true}})
	report := map[string]any{"decision": d, "review": review, "review_error": err != nil, "wechat_sends": 0, "tool_calls": 0, "isolated_memory": true}
	b, _ := json.MarshalIndent(report, "", "  ")
	if e := os.WriteFile(filepath.Join(workspace, "research/evidence/goal1-step6-live-plan.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	if d.Action != "reply" || len(d.Messages) != 2 || err != nil || review.Action != "cancel" {
		t.Fatal("semantic plan / clear interruption failed", d, review, err)
	}
}
