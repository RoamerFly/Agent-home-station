package companion

import (
	"context"
	"testing"
	"wechat-bot/internal/mcptools"
)

type ordinaryGroupProbe struct{ tokens []int }

func (m *ordinaryGroupProbe) Chat(ctx context.Context, system string, turns []map[string]string) (string, error) {
	return m.ChatFunctions(ctx, system, turns, nil)
}
func (m *ordinaryGroupProbe) ChatFunctions(_ context.Context, system string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
	m.tokens = append(m.tokens, inspectContext(system, turns, tools).EstimatedTokens)
	return `{"action":"reply","messages":["周六下午可以。"]}`, nil
}
func TestOrdinaryGroupInputBudget(t *testing.T) {
	model := &ordinaryGroupProbe{}
	for _, text := range []string{"周六下午一起聊会儿吗", "明天几点见面合适", "刚才说的时间可以吗", "今天天气真好", "晚安，明天见", "大家周末有什么安排"} {
		cfg := enabledConfig()
		cfg.Grounding = nil
		cfg.Permissions.ObserveGroups, cfg.Permissions.AutonomousGroups = true, true
		e, err := NewPreview(cfg, model, "合成群聊角色")
		if err != nil {
			t.Fatal(err)
		}
		e.Tools = &policyContextTools{}
		before := len(model.tokens)
		_, err = e.Respond(t.Context(), Input{Channel: "wechat", Account: "fixture", Thread: "fixture@chatroom", Sender: "alice", Mode: "social", Text: text, IsGroup: true, Preview: true, TraceID: "fixture-msg"})
		if err != nil || len(model.tokens) != before+1 {
			t.Fatalf("ordinary conversation calls=%d err=%v", len(model.tokens)-before, err)
		}
	}
	sum := 0
	for _, n := range model.tokens {
		sum += n
	}
	if sum*100 > 41123*70 {
		t.Fatalf("ordinary-group-v1 input %d exceeds 70%% of baseline 41123", sum)
	}
	t.Logf("ordinary-group-v1 samples=%d total=%d mean=%.1f", len(model.tokens), sum, float64(sum)/float64(len(model.tokens)))
}
