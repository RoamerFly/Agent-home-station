package deepseek

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"
)

type thinkingSessionKey struct{}
type transientThought struct {
	answer    [32]byte
	reasoning string
}
type thinkingSession struct {
	sync.Mutex
	prefix string
	serial int
	values map[string]transientThought
	latest map[[32]byte]string
}

// WithThinkingSession keeps provider reasoning only during one live tool loop.
func WithThinkingSession(ctx context.Context) (context.Context, func()) {
	if _, ok := ctx.Value(thinkingSessionKey{}).(*thinkingSession); ok {
		return ctx, func() {}
	}
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	s := &thinkingSession{prefix: hex.EncodeToString(nonce[:]), values: map[string]transientThought{}, latest: map[[32]byte]string{}}
	return context.WithValue(ctx, thinkingSessionKey{}, s), func() { s.Lock(); clear(s.values); clear(s.latest); s.Unlock() }
}
func rememberThinking(ctx context.Context, text, reasoning string) {
	if s, ok := ctx.Value(thinkingSessionKey{}).(*thinkingSession); ok && reasoning != "" {
		s.Lock()
		defer s.Unlock()
		s.serial++
		ref := s.prefix + strconv.Itoa(s.serial)
		key := sha256.Sum256([]byte(text))
		s.values[ref] = transientThought{key, reasoning}
		s.latest[key] = ref
	}
}

// AssistantTurn carries only an opaque session reference, never reasoning text.
func AssistantTurn(ctx context.Context, text string) map[string]string {
	turn := map[string]string{"role": "assistant", "content": text}
	if s, ok := ctx.Value(thinkingSessionKey{}).(*thinkingSession); ok {
		s.Lock()
		defer s.Unlock()
		if ref := s.latest[sha256.Sum256([]byte(text))]; ref != "" {
			turn["transient_thinking_ref"] = ref
		}
	}
	return turn
}
func replayThinking(ctx context.Context, turn map[string]string) string {
	if s, ok := ctx.Value(thinkingSessionKey{}).(*thinkingSession); ok {
		s.Lock()
		defer s.Unlock()
		if entry, exists := s.values[turn["transient_thinking_ref"]]; exists && entry.answer == sha256.Sum256([]byte(turn["content"])) {
			return entry.reasoning
		}
	}
	return ""
}
