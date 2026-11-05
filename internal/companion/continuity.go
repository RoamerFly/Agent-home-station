package companion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
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

// SaveCommitment saves one independently traceable agreement or unfinished
// item. Corrections update this same entry and retain both source chains;
// cancellation is lifecycle state rather than Status=deleted, which is
// reserved for an explicit privacy forget operation.
func (s *Store) SaveCommitment(ctx context.Context, m MemoryEntry) (MemoryEntry, error) {
	if m.Scope == "" || m.Subject == "" || len(m.Sources) == 0 || m.Inference {
		return m, errors.New("commitment requires scope, speaker and explicit sources")
	}
	if m.ID != "" || m.Manual || m.MergeInto != "" || m.Supersedes != "" || len(m.Assertions) != 0 || len(m.ConflictsWith) != 0 {
		return m, errors.New("commitment mutation requires its own source and correction target")
	}
	if m.Corrects == "" && m.CorrectsVersion != 0 || m.Corrects != "" && m.CorrectsVersion < 1 {
		return m, ErrMemoryConflict
	}
	if m.CommitmentState == "" {
		m.CommitmentState = "open"
	}
	switch m.CommitmentState {
	case "open", "ended", "cancelled":
	default:
		return m, errors.New("invalid commitment state")
	}
	m.Kind = "episode"
	m.Continuity = true
	m.Status = "active"
	// Closed commitments remain auditable, but must not consume the small
	// pinned current-continuity budget.
	m.Pinned = m.CommitmentState == "open"
	if EstimateTokens(m.Detail) > 1024 {
		return m, errors.New("commitment candidate exceeds core snapshot budget")
	}

	latestIncoming := time.Time{}
	for _, src := range m.Sources {
		ev, err := s.ReadEvent(ctx, []string{m.Scope}, src.EventID)
		if err != nil {
			return m, err
		}
		stamp, _ := time.Parse(time.RFC3339Nano, ev.At)
		if stamp.After(latestIncoming) {
			latestIncoming = stamp
		}
	}
	expected := 0
	if m.Corrects != "" {
		prior, err := s.ReadMemory(ctx, []string{m.Scope}, m.Corrects)
		if err != nil {
			return m, err
		}
		if !prior.Continuity || prior.CommitmentState == "" || prior.Kind != "episode" || prior.Status != "active" {
			return m, errors.New("correction target is not an active commitment")
		}
		if m.Subject != prior.Subject {
			return m, errors.New("commitment correction must preserve subject")
		}
		if !latestIncoming.After(prior.LastMention) && sameCommitmentUpdate(prior, m) {
			// Replaying a model result after a write but before its maintenance
			// checkpoint committed is harmless and must not create a new revision.
			return prior, nil
		}
		if m.CorrectsVersion != prior.Version {
			return m, ErrMemoryConflict
		}
		if !latestIncoming.After(prior.LastMention) {
			return m, errors.New("commitment correction requires a newer explicit source")
		}
		m.ID = prior.ID
		expected = prior.Version
		m.Sources = appendCommitmentSources(prior.Sources, m.Sources)
	}
	if m.ID == "" {
		root := append([]MemorySource(nil), m.Sources...)
		sort.SliceStable(root, func(i, j int) bool {
			if root[i].EventID == root[j].EventID {
				return root[i].Quote < root[j].Quote
			}
			return root[i].EventID < root[j].EventID
		})
		// The identity deliberately excludes model-written Detail. A replay may
		// phrase the same agreement differently; sources and subject are the
		// durable identity. Multiple agreements from one message therefore need
		// distinct, exact source quotes (otherwise they are one combined item).
		identity := m.Scope + "\x00commitment\x00" + m.Subject
		for _, src := range root {
			identity += "\x00" + src.EventID + "\x00" + src.Quote
		}
		h := sha256.Sum256([]byte(identity))
		m.ID = hex.EncodeToString(h[:])
	}
	// Corrects is a command for this update, not a persisted relationship that
	// could accidentally retarget a later retry.
	m.Corrects = ""
	m.CorrectsVersion = 0
	return s.SaveMemory(ctx, m, expected)
}

func sameCommitmentUpdate(prior, candidate MemoryEntry) bool {
	if prior.Detail != candidate.Detail || prior.Summary != candidate.Summary || prior.CommitmentState != candidate.CommitmentState {
		return false
	}
	seen := make(map[MemorySource]bool, len(prior.Sources))
	for _, src := range prior.Sources {
		seen[src] = true
	}
	for _, src := range candidate.Sources {
		if !seen[src] {
			return false
		}
	}
	return true
}

func appendCommitmentSources(prior, incoming []MemorySource) []MemorySource {
	out := append([]MemorySource(nil), prior...)
	seen := make(map[MemorySource]bool, len(out))
	for _, src := range out {
		seen[src] = true
	}
	for _, src := range incoming {
		if !seen[src] {
			out = append(out, src)
			seen[src] = true
		}
	}
	return out
}
