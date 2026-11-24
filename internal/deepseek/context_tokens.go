package deepseek

import (
	"context"
	"unicode/utf8"
)

// AdditionalContextTokens estimates inputs added by ChatFunctions after the text
// transcript is prepared. Provider usage is authoritative after inference.
func (c *Client) AdditionalContextTokens(ctx context.Context, turns []map[string]string) int {
	total := 0
	effective, err := c.taskReasoningClient(ctx)
	if err == nil && effective.thinkingEnabled {
		for _, turn := range turns {
			if turn["role"] == "assistant" {
				total += estimateReplayText(replayThinking(ctx, turn))
			}
		}
	}
	s, _ := ctx.Value(imageSessionKey{}).(*imageSession)
	if s == nil || !c.SupportsDirectImages() {
		return total
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items)+len(s.pendingFiles) == 0 {
		return total
	}
	total += estimateReplayText(sessionMediaIntro) + 8
	for _, item := range s.items {
		// https://api-docs.deepseek.com/guides/vision/ specifies at most
		// 1024 tokens per image after resizing, independent of encoded size.
		total += 1024 + estimateReplayText(item.label)
	}
	for _, id := range s.pendingFiles {
		// Only the reference is locally visible. Parsed document tokens are
		// determined by the provider after upload.
		total += 8 + estimateReplayText(id)
	}
	return total
}

// Count non-ASCII characters individually and allow one token per three ASCII bytes.
func estimateReplayText(text string) int {
	ascii, other := 0, 0
	for _, r := range text {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			other++
		}
	}
	return other + (ascii+2)/3
}
