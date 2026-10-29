package companion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// Continuity is a single source-backed episode per conversation. It never
// changes the configured persona, scheduling state or real delivery records.
func (s *Store) SaveContinuity(ctx context.Context, m MemoryEntry) (MemoryEntry, error) {
	h := sha256.Sum256([]byte(m.Scope + "\x00continuity"))
	m.ID = hex.EncodeToString(h[:])
	m.Kind = "episode"
	m.Continuity = true
	m.Pinned = true
	m.Status = "active"
	m.Manual = false
	m.Inference = false
	if EstimateTokens(m.Detail) > 1024 {
		return m, errors.New("continuity candidate exceeds core snapshot budget")
	}
	expected := 0
	var at string
	err := s.db.QueryRowContext(ctx, "SELECT version,updated FROM memories WHERE id=? AND scope=?", m.ID, m.Scope).Scan(&expected, &at)
	if err == nil {
		var latest time.Time
		for _, src := range m.Sources {
			ev, e := s.ReadEvent(ctx, []string{m.Scope}, src.EventID)
			if e != nil {
				return m, e
			}
			stamp, _ := time.Parse(time.RFC3339Nano, ev.At)
			if stamp.After(latest) {
				latest = stamp
			}
		}
		old, e := s.ReadMemory(ctx, []string{m.Scope}, m.ID)
		if e == nil && !latest.After(old.LastMention) {
			return old, nil
		}
	}
	return s.SaveMemory(ctx, m, expected)
}
