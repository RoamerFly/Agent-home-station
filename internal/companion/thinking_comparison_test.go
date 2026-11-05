package companion

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/deepseek"
)

// Explicit, small paid comparison. The ordinary test suite never calls it.
func TestFlashThinkingTargetedComparison(t *testing.T) {
	key := os.Getenv("WECHAT_FLASH_COMPARE_KEY_FILE")
	if key == "" {
		t.Skip("explicit local comparison only")
	}
	report := os.Getenv("WECHAT_FLASH_COMPARE_REPORT")
	if report == "" {
		t.Fatal("report path required")
	}
	if _, err := os.Stat(report); err == nil {
		t.Fatal("existing comparison must not be overwritten")
	}
	cases := []struct{ ID, Scene, Draft string }{
		{"unsupported-topic", "call", "刚才视频聊了不少家常吧？"},
		{"unsupported-feeling", "call", "视频聊完一般心里还热乎着呢，都聊了些什么呀？"},
		{"unsupported-visual-detail", "umbrella", "一把撑开的伞。"},
	}
	rows := []map[string]any{}
	save := func() {
		b, _ := json.MarshalIndent(map[string]any{"model": "deepseek-flash", "reasoning_effort": "low", "output_budget": 4096, "method": "same immutable draft and evidence; read-only checking only; no editor, generation or rubric call", "results": rows, "wechat_sends": 0}, "", "  ")
		if err := os.WriteFile(report, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(report), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		store, c, _ := memoryFixture(t)
		in, rc := deepVerifierScene(t, store, c, tc.Scene)
		e := &Engine{Store: store, Config: c, model: &fakeModel{}}
		d, err := e.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{tc.Draft}, ReplyContexts: []ReplyContext{rc}})
		if err != nil {
			t.Fatal(err)
		}
		state := e.groundingReviewState(t.Context(), in, d, false)
		binding, _, err := projectGroundingCheckState(state)
		if err != nil {
			t.Fatal(err)
		}
		for _, enabled := range []bool{false, true} {
			usage := []deepseek.Usage{}
			client, err := deepseek.New(deepseek.Config{KeyFile: key, Model: "deepseek-flash", ThinkingEnabled: &enabled, ReasoningEffort: "low", MaxTokens: 4096, ToolMaxTokens: 4096, OnUsage: func(u deepseek.Usage) { usage = append(usage, u) }})
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
			answer, callErr := client.ChatGrounding(ctx, groundingCheckPolicy, []map[string]string{{"role": "user", "content": string(binding.ReviewInput)}}, deepseek.GroundingOptions{Model: "deepseek-flash", MaxTokens: 4096, ReasoningEffort: "low"})
			cancel()
			row := map[string]any{"id": tc.ID, "thinking_enabled": enabled, "draft": tc.Draft, "evidence": binding.ReviewInput, "usage": usage, "seconds": time.Since(started).Seconds(), "expected": "reject"}
			if callErr != nil {
				row["error"] = callErr.Error()
				rows = append(rows, row)
				save()
				t.Fatal(callErr)
			}
			verdict, err := decodeGroundingVerdict(answer, "comparison", d.Messages, nil, binding.EvidenceTable...)
			if err != nil {
				row["protocol_error"] = err.Error()
			} else {
				row["verdict"] = verdict.Verdict
				row["detected_error"] = verdict.Verdict == "reject"
				row["units"] = verdict.Units
			}
			rows = append(rows, row)
			save()
		}
	}
}
