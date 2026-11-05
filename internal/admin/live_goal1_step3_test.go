package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

func TestLiveGoal1ProactiveGroupAndFriend(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_GOAL1_ROOT"), os.Getenv("WECHAT_GOAL1_WORKSPACE")
	if root == "" || workspace == "" {
		t.Skip("explicit isolated provider regression")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal("private configuration unavailable")
	}
	f := setup(t)
	provider, err := f.manager.makeModel(c)
	if err != nil {
		t.Fatal("provider unavailable")
	}
	config := c.Companion
	config.Enabled = true
	config.Permissions.Proactive = true
	config.Permissions.Remember = true
	config.QuietStart = "00:00"
	config.QuietEnd = "00:00"
	config.ProactiveThreads = []string{"hypothetical@chatroom", "hypothetical-friend"}
	config.ProactiveMaxPerDay = 2
	config.ProactiveRules = []companion.ProactiveRule{{Thread: config.ProactiveThreads[0], IdleMinutes: 5, MaxPerDay: 1}, {Thread: config.ProactiveThreads[1], IdleMinutes: 10, MaxPerDay: 1}}
	store, err := companion.NewStore(t.TempDir())
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, thread := range config.ProactiveThreads {
		if err = store.Observe(companion.Input{Channel: "isolated", Account: "diagnostic", Thread: thread, Mode: "direct", Text: "刚才聊到鲸鱼和海洋音乐，大家说晚点有空再聊。", Sender: "假想用户"}, config, time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	chats := 0
	model := previewModel{chat: func(ctx context.Context, system string, turns []map[string]string) (string, error) {
		chats++
		return provider.Chat(ctx, system, turns)
	}}
	engine, err := companion.New(config, store, model, c.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	decisions := []map[string]any{}
	for _, input := range engine.Proactive("isolated", "diagnostic") {
		if err = engine.ClaimProactive(input); err != nil {
			t.Fatal(err)
		}
		decision, callErr := engine.Respond(ctx, input)
		if callErr != nil {
			t.Fatal("live proactive judgement failed", deepseek.UserMessage(callErr))
		}
		if decision.Action != "reply" && decision.Action != "silent" {
			t.Fatal("invalid proactive decision", decision.Action, decision.Reason)
		}
		decisions = append(decisions, map[string]any{"is_group": input.IsGroup, "action": decision.Action, "reason": decision.Reason})
	}
	_, claims := store.Counters(config, time.Now())
	if len(decisions) != 2 || chats != 2 || claims != 2 || len(engine.Proactive("isolated", "diagnostic")) != 0 {
		t.Fatal("bad schedule or claims", len(decisions), chats, claims)
	}
	report := map[string]any{"configured_provider_live": true, "isolated_model_chats": chats, "decisions": decisions, "proactive_claims": claims, "new_candidates_after_claim": 0, "production_config_and_memory_modified": false, "wechat_sends": 0}
	data, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(filepath.Join(workspace, "research/evidence/goal1-step3-live-model.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
}
