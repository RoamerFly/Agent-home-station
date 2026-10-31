package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"wechat-bot/internal/companion"
)

func TestThinkingConfigRoundTripAndSharedReview(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, effort := range []string{"low", "high", "max"} {
			c := defaults()
			c.ThinkingEnabled = enabled
			c.ReasoningEffort = effort
			c.Companion.Grounding = &companion.GroundingSettings{Model: "legacy-review-model", ReasoningEffort: "max", MaxTokens: 4096, TimeoutSeconds: 120}
			if err := validate(c); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			raw, _ := json.Marshal(c)
			if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			saved, err := loadConfig(dir)
			if err != nil || saved.ThinkingEnabled != enabled || saved.ReasoningEffort != effort {
				t.Fatal("thinking settings lost", err)
			}
			shared := saved.sharedCompanion().GroundingConfig()
			if shared.Model != saved.Model || shared.ReasoningEffort != effort || c.Companion.Grounding.Model != "legacy-review-model" {
				t.Fatal("review diverged or source config mutated", shared)
			}
		}
	}
	c := defaults()
	c.ReasoningEffort = "medium"
	if validate(c) == nil {
		t.Fatal("unsupported effort silently remapped")
	}
}
