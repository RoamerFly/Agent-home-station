package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

func TestGroundingReviewConfigRoundTripAndClone(t *testing.T) {
	c := defaults()
	c.Companion.Grounding = &companion.GroundingSettings{MaxTokens: 16384, TimeoutSeconds: 180, ReasoningEffort: "low"}
	cloned := cloneConfig(c)
	cloned.Companion.Grounding.MaxTokens = 32768
	if c.Companion.Grounding.MaxTokens != 16384 {
		t.Fatal("cloned review settings share mutable state")
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Companion.GroundingConfig() != *c.Companion.Grounding {
		t.Fatalf("review options changed during config round trip: %+v", loaded.Companion.GroundingConfig())
	}
}

func TestManagementPreviewUsesDedicatedGroundingTransport(t *testing.T) {
	called := false
	model := previewGroundingModel{
		previewModel: previewModel{chat: func(context.Context, string, []map[string]string) (string, error) {
			t.Fatal("review fell back to conversational chat")
			return "", nil
		}},
		grounding: func(_ context.Context, system string, messages []map[string]string, options deepseek.GroundingOptions) (string, error) {
			called = true
			if system != "review json" || len(messages) != 1 || options.MaxTokens != 16384 {
				t.Fatal("review request or options were not forwarded")
			}
			return `{"bubbles":[]}`, nil
		},
	}
	if _, err := model.ChatGrounding(context.Background(), "review json", []map[string]string{{"role": "user", "content": "synthetic"}}, deepseek.GroundingOptions{MaxTokens: 16384}); err != nil || !called {
		t.Fatal("dedicated review transport not used", err)
	}
}
