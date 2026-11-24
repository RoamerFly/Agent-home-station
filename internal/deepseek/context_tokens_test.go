package deepseek

import (
	"context"
	"testing"
)

func TestAdditionalContextTokens(t *testing.T) {
	ctx, closeThinking := WithThinkingSession(context.Background())
	defer closeThinking()
	ctx, closeImages := WithImageSession(ctx)
	defer closeImages()
	c := &Client{model: "deepseek-flash", thinkingEnabled: true}
	rememberThinking(ctx, "answer", "合成推理")
	turns := []map[string]string{AssistantTurn(ctx, "answer")}
	if got := c.AdditionalContextTokens(ctx, turns); got != 4 {
		t.Fatalf("reasoning estimate %d", got)
	}
	off, err := WithTaskReasoning(ctx, TaskReasoning{Mode: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.AdditionalContextTokens(off, turns); got != 0 {
		t.Fatal(got)
	}
	im := Image{Data: []byte("\x89PNG\r\n\x1a\nsynthetic")}
	for i := 0; i < 2; i++ {
		if err := AttachImage(ctx, "样图", im); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := c.AdditionalContextTokens(ctx, turns), 4+1024+2+8+estimateReplayText(sessionMediaIntro); got != want {
		t.Fatalf("media estimate %d, want %d", got, want)
	}
	consumeSessionMedia(ctx)
	if got := c.AdditionalContextTokens(ctx, turns); got != 4 {
		t.Fatalf("after consume %d", got)
	}
}
