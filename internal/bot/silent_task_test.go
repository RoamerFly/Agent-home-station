package bot

import (
	"context"
	"testing"
	"wechat-bot/internal/companion"
)

func TestSilentTaskProgressNeverSubmits(t *testing.T) {
	w := &Worker{}
	sent, err := w.generationProgress(context.Background(), nil, nil, companion.Input{SilentExecution: true}, "any progress wording")
	if sent || err != nil {
		t.Fatal("silent execution emitted progress", err)
	}
}
