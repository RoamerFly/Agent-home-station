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

// ErrInvalidExperienceQuery identifies caller-correctable view or filter
// parameters. Stale cursors and evidence revisions remain ErrMemoryConflict.
var ErrInvalidExperienceQuery = errors.New("invalid experience query")

// ExperienceQuery is the deliberately small, current-conversation query used
// by the relationship workbench.  It is not a second memory store: entries
// remain editable through the normal memory APIs and this view is recalculated
// from their current, source-validated revision on every read.
type ExperienceQuery struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	View   string `json:"view,omitempty"`
	Person string `json:"person,omitempty"`
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
	Assertions      []MemoryAssertion  `json:"assertions,omitempty"`
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
	q.View = strings.TrimSpace(q.View)
	q.Person = strings.TrimSpace(q.Person)
	if !validScopeID(scope) || q.Limit < 0 {
		return p, ErrInvalidExperienceQuery
	}
	switch q.View {
	case "", "relations", "open_commitments":
	default:
		return p, ErrInvalidExperienceQuery
	}
	if q.Limit == 0 {
		q.Limit = 24
	}
	if q.Limit == int(^uint(0)>>1) {
		return p, ErrInvalidExperienceQuery
	}
	p.EffectiveLimit = q.Limit

	epoch, err := s.DataEpoch(ctx)
	if err != nil {
		return p, err
	}
	key, _ := json.Marshal([]string{scope, epoch, "shared_experience_v2", q.View, q.Person})
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
	where := `m.scope=? AND m.status='active'
		AND COALESCE(json_extract(m.payload,'$.manual'),0)=0
		AND COALESCE(json_extract(m.payload,'$.inference'),0)=0
		AND json_type(m.payload,'$.sources')='array' AND json_array_length(m.payload,'$.sources')>0
		AND EXISTS(SELECT 1 FROM memory_sources has_source WHERE has_source.memory_id=m.id)
		AND NOT EXISTS(SELECT 1 FROM memory_sources ms JOIN source_conflicts sc ON sc.event_id=ms.event_id WHERE ms.memory_id=m.id)
		AND NOT EXISTS(SELECT 1 FROM memory_sources ms JOIN memory_blocks mb ON mb.event_id=ms.event_id AND mb.scope=m.scope WHERE ms.memory_id=m.id)`
	const relatedPerson = `EXISTS(SELECT 1 FROM json_each(m.payload,'$.assertions') a
		WHERE json_extract(a.value,'$.state') IN ('explicit','reported','corrected')
		AND (json_extract(a.value,'$.subject')=? OR
			(json_extract(a.value,'$.object_basis')!='literal' AND json_extract(a.value,'$.object')=?)))`
	args := []any{scope}
	switch q.View {
	case "relations":
		where += ` AND m.kind IN ('episode','fact')
			AND json_type(m.payload,'$.assertions')='array' AND json_array_length(m.payload,'$.assertions')>0`
		if q.Person != "" {
			where += " AND " + relatedPerson
			args = append(args, q.Person, q.Person)
		}
	case "open_commitments":
		where += ` AND m.kind='episode' AND COALESCE(json_extract(m.payload,'$.continuity'),0)=1
			AND COALESCE(json_extract(m.payload,'$.commitment_state'),'')='open'`
		if q.Person != "" {
			where += " AND m.subject=?"
			args = append(args, q.Person)
		}
	default:
		where += ` AND m.kind='episode'
			AND (COALESCE(json_extract(m.payload,'$.continuity'),0)=0
				OR COALESCE(json_extract(m.payload,'$.commitment_state'),'') IN ('open','ended','cancelled'))`
		if q.Person != "" {
			where += " AND (m.subject=? OR " + relatedPerson + ")"
			args = append(args, q.Person, q.Person, q.Person)
		}
	}
	where += reliableMemorySourcesSQL("m")
	listSQL := "SELECT m.payload,m.version FROM memories m WHERE " + where + " ORDER BY m.updated DESC,m.id DESC LIMIT ? OFFSET ?"
	nextOffset := offset
	batchSize := 64
	if q.Limit < batchSize {
		batchSize = q.Limit + 1
	}
	for {
		queryArgs := append(append([]any(nil), args...), batchSize, nextOffset)
		rows, queryErr := s.db.QueryContext(ctx, listSQL, queryArgs...)
		if queryErr != nil {
			return p, queryErr
		}
		type storedEntry struct {
			entry   MemoryEntry
			version int
		}
		entries := make([]storedEntry, 0, batchSize)
		for rows.Next() {
			var raw []byte
			var version int
			if err = rows.Scan(&raw, &version); err != nil {
				rows.Close()
				return p, err
			}
			var entry MemoryEntry
			if err = json.Unmarshal(raw, &entry); err != nil {
				rows.Close()
				return p, err
			}
			entries = append(entries, storedEntry{entry: entry, version: version})
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return p, err
		}
		if err = rows.Close(); err != nil {
			return p, err
		}
		if len(entries) == 0 {
			break
		}
		for index, stored := range entries {
			recordOffset := nextOffset + index
			stored.entry.Version = stored.version
			item, buildErr := s.sharedExperience(ctx, stored.entry, q.View, q.Person)
			if errors.Is(buildErr, ErrMemoryConflict) || errors.Is(buildErr, sql.ErrNoRows) {
				// Sources can change between the SQL prefilter and exact assertion
				// verification. They never consume a page slot.
				continue
			}
			if buildErr != nil {
				return p, buildErr
			}
			if len(p.Items) == q.Limit {
				p.HasMore = true
				nextOffset = recordOffset
				break
			}
			p.Items = append(p.Items, item)
		}
		if p.HasMore {
			break
		}
		nextOffset += len(entries)
		if len(entries) < batchSize {
			break
		}
	}
	// Avoid stitching memory rows from one version together with source events
	// from another. The caller can retry with a new page cursor.
	if after, afterErr := s.DataEpoch(ctx); afterErr != nil || after != epoch {
		if afterErr != nil {
			return p, afterErr
		}
		return p, ErrMemoryConflict
	}
	if p.HasMore {
		b, _ := json.Marshal(struct {
			Hash   string
			Offset int
		}{Hash: hex.EncodeToString(hash[:]), Offset: nextOffset})
		p.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return p, nil
}

func (s *Store) sharedExperience(ctx context.Context, entry MemoryEntry, view, person string) (SharedExperience, error) {
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
	events := make(map[string]MemoryEvent, len(entry.Sources))
	for _, source := range entry.Sources {
		event, found := events[source.EventID]
		if !found {
			var err error
			event, err = s.ReadEvent(ctx, []string{entry.Scope}, source.EventID)
			if err != nil {
				return SharedExperience{}, err
			}
			events[source.EventID] = event
		}
		if strings.TrimSpace(source.Quote) == "" || !strings.Contains(event.Text, source.Quote) {
			return SharedExperience{}, ErrMemoryConflict
		}
		item.Sources = append(item.Sources, ExperienceSource{EventID: source.EventID, Quote: source.Quote, Sender: event.Sender, SenderName: event.SenderName, Role: event.Role, At: event.At, Revision: event.Revision})
	}
	assertions := verifiedExperienceAssertions(entry.Assertions, person, events)
	if view == "relations" && len(assertions) == 0 {
		return SharedExperience{}, ErrMemoryConflict
	}
	item.Assertions = assertions
	if view == "relations" {
		// A relation hit is the verified assertion, not every prose detail or
		// unrelated source carried by the same memory document. Keep only the
		// selected assertions' evidence for auditability.
		item.Detail, item.Summary = "", ""
		allowed := map[string]bool{}
		for _, assertion := range assertions {
			for _, source := range assertion.Sources {
				allowed[source.EventID+"\x00"+source.Quote] = true
			}
		}
		filteredSources := item.Sources[:0]
		for _, source := range item.Sources {
			if allowed[source.EventID+"\x00"+source.Quote] {
				filteredSources = append(filteredSources, source)
			}
		}
		item.Sources = filteredSources
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

// verifiedExperienceAssertions returns only current, source-verified
// structured assertions. Relation cards must never infer a connection from a
// detail string, nickname, or an unrelated assertion in the same document.
func verifiedExperienceAssertions(assertions []MemoryAssertion, person string, events map[string]MemoryEvent) []MemoryAssertion {
	var out []MemoryAssertion
	for _, assertion := range assertions {
		if assertion.State != "explicit" && assertion.State != "reported" && assertion.State != "corrected" {
			continue
		}
		if person != "" && assertion.Subject != person && (assertion.ObjectBasis == "literal" || assertion.Object != person) {
			continue
		}
		if experienceAssertionSourcesCurrent(assertion, events) {
			out = append(out, assertion)
		}
	}
	return out
}

func experienceAssertionSourcesCurrent(a MemoryAssertion, events map[string]MemoryEvent) bool {
	if a.Subject == "" || a.Predicate == "" || a.Object == "" || a.AssertedBy == "" || len(a.Sources) == 0 {
		return false
	}
	for _, source := range a.Sources {
		event, ok := events[source.EventID]
		if !ok || source.EventRevision < 1 || event.Revision != source.EventRevision || event.Sender != a.AssertedBy ||
			strings.TrimSpace(source.Quote) == "" || !strings.Contains(event.Text, source.Quote) {
			return false
		}
		switch event.Role {
		case "user":
			if event.UserTextState == "unknown" {
				return false
			}
		case "admin_correction":
			// SaveSourcedAdminCorrection links both events to the same entry.
			// Forgetting or revising the original also invalidates the correction.
			ref := event.QuotedSource
			if a.State != "corrected" || ref == nil {
				return false
			}
			original, found := events[ref.EventID]
			if !found || original.Revision != ref.Revision {
				return false
			}
		default:
			return false
		}
		turn := Turn{Sender: event.Sender, SenderName: event.SenderName, QuotedSource: event.QuotedSource, Addressees: event.Addressees}
		if !assertionBinding(a.Subject, a.SubjectBasis, a.SubjectText, a, turn, source.Quote, false) ||
			!assertionBinding(a.Object, a.ObjectBasis, a.ObjectText, a, turn, source.Quote, true) ||
			a.PredicateText == "" || !strings.Contains(source.Quote, a.PredicateText) {
			return false
		}
	}
	return true
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
