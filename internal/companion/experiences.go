package companion

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ExperienceQuery is the deliberately small, current-conversation query used
// by the relationship workbench.  It is not a second memory store: entries
// remain editable through the normal memory APIs and this view is recalculated
// from their current, source-validated revision on every read.
type ExperienceQuery struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// ExperienceSource makes the provenance visible without exposing unrelated
// conversation rows.  Quote is the exact memory source quote; callers can use
// EventID with the existing authorized source-reader for the complete event.
type ExperienceSource struct {
	EventID    string `json:"event_id"`
	Quote      string `json:"quote"`
	Sender     string `json:"sender,omitempty"`
	SenderName string `json:"sender_name,omitempty"`
	Role       string `json:"role,omitempty"`
	At         string `json:"at,omitempty"`
	Revision   int    `json:"revision,omitempty"`
}

// SharedExperience is a view of an active, sourced episode or an explicit
// commitment.  Perspective prevents a group member's report from being shown
// as if it were the fish's own lived experience.
type SharedExperience struct {
	ID              string             `json:"id"`
	Version         int                `json:"version"`
	Kind            string             `json:"kind"`
	Subject         string             `json:"subject"`
	Detail          string             `json:"detail"`
	Summary         string             `json:"summary,omitempty"`
	Created         string             `json:"created,omitempty"`
	Updated         string             `json:"updated,omitempty"`
	Continuity      bool               `json:"continuity,omitempty"`
	CommitmentState string             `json:"commitment_state,omitempty"`
	Perspective     string             `json:"perspective"`
	Sources         []ExperienceSource `json:"sources"`
}

type SharedExperiencePage struct {
	Items          []SharedExperience `json:"items"`
	NextCursor     string             `json:"next_cursor,omitempty"`
	HasMore        bool               `json:"has_more"`
	EffectiveLimit int                `json:"effective_limit"`
	Coverage       string             `json:"coverage"`
}

// ListSharedExperiences returns only source-backed, currently valid material
// for one authorized conversation.  Inference, hand-written notes, stale
// generic continuity and unavailable/forgotten/conflicted evidence are not a
// shared experience and cannot leak into the palace through this endpoint.
func (s *Store) ListSharedExperiences(ctx context.Context, scope string, q ExperienceQuery) (SharedExperiencePage, error) {
	p := SharedExperiencePage{Items: []SharedExperience{}, Coverage: "verified_memory"}
	if !validScopeID(scope) || q.Limit < 0 {
		return p, errors.New("invalid experience query")
	}
	if q.Limit == 0 {
		q.Limit = 24
	}
	if q.Limit == int(^uint(0)>>1) {
		return p, errors.New("experience page size exceeds integer range")
	}
	p.EffectiveLimit = q.Limit

	epoch, err := s.DataEpoch(ctx)
	if err != nil {
		return p, err
	}
	key, _ := json.Marshal([]string{scope, epoch, "shared_experience_v1"})
	hash := sha256.Sum256(key)
	offset := 0
	if q.Cursor != "" {
		var c struct {
			Hash   string
			Offset int
		}
		b, decodeErr := base64.RawURLEncoding.DecodeString(q.Cursor)
		if decodeErr != nil || json.Unmarshal(b, &c) != nil || c.Hash != hex.EncodeToString(hash[:]) || c.Offset < 0 {
			return p, ErrMemoryConflict
		}
		offset = c.Offset
	}

	// Explicit commitments are continuity, but generic old continuity snapshots
	// are not reliably an experience.  The latter is therefore excluded unless
	// it carries a real commitment lifecycle state.
	where := `m.scope=? AND m.kind='episode' AND m.status='active'
		AND COALESCE(json_extract(m.payload,'$.manual'),0)=0
		AND COALESCE(json_extract(m.payload,'$.inference'),0)=0
		AND json_type(m.payload,'$.sources')='array' AND json_array_length(m.payload,'$.sources')>0
		AND (COALESCE(json_extract(m.payload,'$.continuity'),0)=0
			OR COALESCE(json_extract(m.payload,'$.commitment_state'),'') IN ('open','ended','cancelled'))
		AND EXISTS(SELECT 1 FROM memory_sources has_source WHERE has_source.memory_id=m.id)
		AND NOT EXISTS(SELECT 1 FROM memory_sources ms JOIN source_conflicts sc ON sc.event_id=ms.event_id WHERE ms.memory_id=m.id)
		AND NOT EXISTS(SELECT 1 FROM memory_sources ms JOIN memory_blocks mb ON mb.event_id=ms.event_id AND mb.scope=m.scope WHERE ms.memory_id=m.id)`
	where += reliableMemorySourcesSQL("m")
	rows, err := s.db.QueryContext(ctx, "SELECT m.payload,m.version FROM memories m WHERE "+where+" ORDER BY m.updated DESC,m.id DESC LIMIT ? OFFSET ?", scope, q.Limit+1, offset)
	if err != nil {
		return p, err
	}
	// Do not preallocate from caller-controlled Limit; SQLite still pages the
	// rows and a valid large request must not turn into a memory allocation.
	entries := []MemoryEntry{}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var version int
		if err = rows.Scan(&raw, &version); err != nil {
			return p, err
		}
		var entry MemoryEntry
		if err = json.Unmarshal(raw, &entry); err != nil {
			return p, err
		}
		entry.Version = version
		entries = append(entries, entry)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return p, err
	}
	// SQLite is deliberately configured with one connection. Close the page
	// cursor before resolving its event provenance, otherwise ReadEvent waits
	// for a connection held by this same goroutine.
	if err = rows.Close(); err != nil {
		return p, err
	}
	for _, entry := range entries {
		item, buildErr := s.sharedExperience(ctx, entry)
		if buildErr != nil {
			// Sources may disappear between the validated page query and the
			// event lookup. Do not return a half-grounded card.
			if errors.Is(buildErr, ErrMemoryConflict) {
				return p, ErrMemoryConflict
			}
			return p, buildErr
		}
		p.Items = append(p.Items, item)
	}
	// Avoid stitching memory rows from one version together with source events
	// from another. The caller can retry with a new page cursor.
	if after, afterErr := s.DataEpoch(ctx); afterErr != nil || after != epoch {
		if afterErr != nil {
			return p, afterErr
		}
		return p, ErrMemoryConflict
	}
	p.HasMore = len(p.Items) > q.Limit
	if p.HasMore {
		p.Items = p.Items[:q.Limit]
		b, _ := json.Marshal(struct {
			Hash   string
			Offset int
		}{Hash: hex.EncodeToString(hash[:]), Offset: offset + len(p.Items)})
		p.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return p, nil
}

func (s *Store) sharedExperience(ctx context.Context, entry MemoryEntry) (SharedExperience, error) {
	if len(entry.Sources) == 0 {
		return SharedExperience{}, ErrMemoryConflict
	}
	item := SharedExperience{ID: entry.ID, Version: entry.Version, Kind: entry.Kind, Subject: entry.Subject, Detail: entry.Detail, Summary: entry.Summary, Continuity: entry.Continuity, CommitmentState: entry.CommitmentState, Perspective: "direct_experience", Sources: make([]ExperienceSource, 0, len(entry.Sources))}
	if !entry.Created.IsZero() {
		item.Created = entry.Created.UTC().Format(time.RFC3339Nano)
	}
	if !entry.Updated.IsZero() {
		item.Updated = entry.Updated.UTC().Format(time.RFC3339Nano)
	}
	for _, source := range entry.Sources {
		event, readErr := s.ReadEvent(ctx, []string{entry.Scope}, source.EventID)
		if errors.Is(readErr, sql.ErrNoRows) {
			return SharedExperience{}, ErrMemoryConflict
		}
		if readErr != nil {
			return SharedExperience{}, readErr
		}
		if strings.TrimSpace(source.Quote) == "" || !strings.Contains(event.Text, source.Quote) {
			return SharedExperience{}, ErrMemoryConflict
		}
		item.Sources = append(item.Sources, ExperienceSource{EventID: source.EventID, Quote: source.Quote, Sender: event.Sender, SenderName: event.SenderName, Role: event.Role, At: event.At, Revision: event.Revision})
	}
	if entry.CommitmentState != "" {
		item.Perspective = "commitment"
	} else if s.experienceScopeIsGroup(entry.Scope) {
		// Keep the same current, source-validated interpretation that chat
		// recall receives. Sources make the speaker and literal wording visible;
		// replacing Detail here would make a corrected relationship appear as a
		// different version in the workbench than in a later conversation.
		item.Perspective = "source_statement"
	} else if memoryOnlyReported(entry.Assertions) {
		item.Perspective = "reported_statement"
	}
	return item, nil
}

func (s *Store) experienceScopeIsGroup(id string) bool {
	s.mu.Lock()
	scope, ok := s.data.Scopes[id]
	s.mu.Unlock()
	return ok && strings.HasSuffix(scope.Thread, "@chatroom")
}

func memoryOnlyReported(assertions []MemoryAssertion) bool {
	if len(assertions) == 0 {
		return false
	}
	for _, assertion := range assertions {
		if assertion.State != "reported" {
			return false
		}
	}
	return true
}
