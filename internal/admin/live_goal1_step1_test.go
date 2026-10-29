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

// Only this opt-in test reads the configured provider. Conversation state is
// isolated, and fakeNative panics if any WeChat send is attempted.
func TestLiveGoal1ModelQuestionJudgement(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_GOAL1_ROOT"), os.Getenv("WECHAT_GOAL1_WORKSPACE")
	if root == "" || workspace == "" {
		t.Skip("explicit isolated live-provider regression")
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
	config := c.Companion
	config.Enabled = true
	config.Permissions.ObserveGroups = true
	config.Permissions.AutonomousGroups = true
	config.QuietStart = "00:00"
	config.QuietEnd = "00:00"
	config.MaxRepliesPerHour = 30
	store, err := companion.NewStore(t.TempDir())
	if opened := store; opened != nil { t.Cleanup(func(){ _ = opened.Close() }) }
	if err != nil {
		t.Fatal(err)
	}
	chats := 0
	questionReview := false
	model := previewModel{chat: func(ctx context.Context, system string, turns []map[string]string) (string, error) {
		chats++
		var current map[string]any
		_ = json.Unmarshal([]byte(turns[len(turns)-1]["content"]), &current)
		if current["text"] == "你目前可调用的mcp工具有哪些" {
			questionReview = current["review_required"] == true && current["directed"] == false
		}
		return provider.Chat(ctx, system, turns)
	}}
	engine, err := companion.New(config, store, model, c.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var requestEvents []map[string]any
	ctx = deepseek.WithRequestObserver(ctx, func(event string, d deepseek.Diagnostics) {
		requestEvents = append(requestEvents, map[string]any{"event": event, "code": d.Code, "http_status": d.HTTPStatus, "attempts": d.Attempts})
	})
	input := companion.Input{Channel: "isolated", Account: "diagnostic", Thread: "hypothetical-group", Sender: "假想用户", Mode: "social", IsGroup: true, Text: "还在加功能[呲牙]"}
	first, err := engine.Respond(ctx, input)
	if err != nil {
		t.Fatal("fresh model response failed", deepseek.UserMessage(err))
	}
	if first.Action != "reply" && first.Action != "silent" {
		t.Fatal("model did not return a valid decision", first.Action)
	}
	// Ensure the second question is evaluated while the normal social decision
	// interval is active, regardless of the first model request's latency.
	if err := store.ClaimDecision(input, config, time.Now()); err != nil {
		t.Fatal(err)
	}
	input.Text = "你目前可调用的mcp工具有哪些"
	second, err := engine.Respond(ctx, input)
	if err != nil {
		t.Fatal("question evaluation failed", deepseek.UserMessage(err))
	}
	if chats != 2 || !questionReview || second.Reason == "decision_cooldown" || second.Action != "reply" && second.Action != "silent" {
		t.Fatal("question did not reach model judgement", chats, questionReview, second.Action, second.Reason)
	}
	report := map[string]any{"configured_provider_live": true, "isolated_model_chats": chats, "fresh_ambient_decision": first.Action, "question_decision": second.Action, "question_reason": second.Reason, "question_reviewed_during_cooldown": questionReview, "verified_mention_fabricated": false, "wechat_sends": 0, "production_memory_modified": false, "request_events": requestEvents}
	data, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(filepath.Join(workspace, "research/evidence/goal1-step1-live-model.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
}
