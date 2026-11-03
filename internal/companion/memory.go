package companion

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type MemorySource struct {
	EventID string `json:"event_id"`
	Quote   string `json:"quote"`
}
type MemoryEntry struct {
	Assertions      []MemoryAssertion `json:"assertions,omitempty"`
	EventYear       int               `json:"event_year,omitempty"`
	DetailBytes     int               `json:"detail_bytes,omitempty"`
	DetailExcerpted bool              `json:"detail_excerpted,omitempty"`
	SourceCount     int               `json:"source_count,omitempty"`
	Continuity      bool              `json:"continuity,omitempty"`
	MergeInto       string            `json:"merge_into,omitempty"`
	ConflictsWith   []string          `json:"conflicts_with,omitempty"`
	ID              string            `json:"id"`
	Scope           string            `json:"scope"`
	Subject         string            `json:"subject"`
	Kind            string            `json:"kind"`
	Detail          string            `json:"detail"`
	Summary         string            `json:"summary"`
	Keywords        []string          `json:"keywords"`
	Sources         []MemorySource    `json:"sources"`
	Status          string            `json:"status"`
	Version         int               `json:"version"`
	Pinned          bool              `json:"pinned"`
	Inference       bool              `json:"inference"`
	Manual          bool              `json:"manual"`
	Supersedes      string            `json:"supersedes,omitempty"`
	EventTime       string            `json:"event_time,omitempty"`
	Created         time.Time         `json:"created"`
	Updated         time.Time         `json:"updated"`
	LastMention     time.Time         `json:"last_mention"`
}
type MemoryHit struct {
	Entry  *MemoryEntry `json:"entry,omitempty"`
	Event  *MemoryEvent `json:"event,omitempty"`
	Reason string       `json:"reason"`
	Score  float64      `json:"score"`
}
type MemoryEvent struct {
	TextBytes        int                `json:"text_bytes,omitempty"`
	ObservationCount int                `json:"observation_count,omitempty"`
	ObservationBytes int                `json:"observation_bytes,omitempty"`
	UserTextState    string             `json:"user_text_state,omitempty"`
	Source           *SourceRef         `json:"source,omitempty"`
	QuotedSource     *SourceRef         `json:"quoted_source,omitempty"`
	Addressees       []string           `json:"addressees,omitempty"`
	IncomingMedia    []IncomingMediaRef `json:"incoming_media,omitempty"`
	SenderName       string             `json:"sender_name,omitempty"`
	Media            *MediaMemory       `json:"media,omitempty"`
	ID               string             `json:"id"`
	Scope            string             `json:"scope"`
	Trace            string             `json:"message_id"`
	Role             string             `json:"role"`
	Sender           string             `json:"sender"`
	At               string             `json:"at"`
	Text             string             `json:"text"`
	Hash             string             `json:"hash"`
	Complete         bool               `json:"complete"`
	Revision         int                `json:"revision"`
	Seq              int64              `json:"seq"`
}
type MemoryQuery struct {
	BeforeMessageID string    `json:"before_message_id,omitempty"`
	MediaKind       string    `json:"media_kind,omitempty"`
	Order           string    `json:"order,omitempty"`
	MetadataOnly    bool      `json:"metadata_only,omitempty"`
	EventYear       int       `json:"event_year,omitempty"`
	TimeBasis       string    `json:"time_basis,omitempty"`
	MinSimilarity   *float64  `json:"min_similarity,omitempty"`
	Pinned          bool      `json:"pinned,omitempty"`
	Scopes          []string  `json:"-"`
	Query           string    `json:"query"`
	Kind            string    `json:"kind,omitempty"`
	Subject         string    `json:"subject,omitempty"`
	Since           string    `json:"since,omitempty"`
	Before          string    `json:"before,omitempty"`
	Cursor          string    `json:"cursor,omitempty"`
	Offset          int       `json:"offset,omitempty"`
	Limit           int       `json:"limit,omitempty"`
	Originals       bool      `json:"originals,omitempty"`
	Vector          []float32 `json:"-"`
	Space           string    `json:"-"`
}
type MemoryPage struct {
	CoverageScopes map[string]string `json:"coverage_scopes,omitempty"`
	Items          []MemoryHit       `json:"items"`
	NextCursor     string            `json:"next_cursor,omitempty"`
	NextOffset     int               `json:"next_offset"`
	HasMore        bool              `json:"has_more"`
	Coverage       string            `json:"coverage"`
	Degraded       string            `json:"degraded,omitempty"`
	EffectiveLimit int               `json:"effective_limit"`
}

type memoryConflictError struct{}

func (memoryConflictError) Error() string {
	return "memory revision changed; read current revision before updating"
}
func (memoryConflictError) SafeToolFailure() (string, string) {
	return "memory_revision_or_cursor_changed", "记忆版本或分页快照已变化；重新查询并按返回的新版本或游标续读，不表示网络连接失败"
}

var ErrMemoryConflict error = memoryConflictError{}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }
func stringsArgs(v []string) []any {
	a := make([]any, len(v))
	for i := range v {
		a[i] = v[i]
	}
	return a
}

// SaveMemory validates every source inside the same transaction as the write.
// Automatic candidates cannot promote assistant prose or tool observations to
// a user's fact, or use a different group/person as evidence.
func (s *Store) SaveMemory(ctx context.Context, in MemoryEntry, expected int) (MemoryEntry, error) {
	return s.saveMemory(ctx, in, expected, nil)
}

// Existing transactions are used only by atomic management-source corrections.
// Their caller owns the mutex, commit and in-memory scope publication.
func (s *Store) saveMemory(ctx context.Context, in MemoryEntry, expected int, existing *sql.Tx) (MemoryEntry, error) {
	in.DetailBytes = 0
	in.DetailExcerpted = false
	in.SourceCount = 0
	if existing == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	if !validScopeID(in.Scope) || strings.TrimSpace(in.Detail) == "" {
		return in, errors.New("memory scope and content required")
	}
	switch in.Kind {
	case "fact", "preference", "episode", "lesson":
	default:
		return in, errors.New("invalid memory kind")
	}
	if in.Status == "" {
		in.Status = "active"
	}
	switch in.Status {
	case "active", "conflicted", "deleted":
	default:
		return in, errors.New("invalid editable memory status")
	}
	if len(in.Sources) == 0 && !in.Manual {
		return in, errors.New("memory requires verifiable sources")
	}
	if in.Manual && in.Subject == "" {
		return in, errors.New("manual note requires verified requester")
	}
	tx := existing
	var e error
	if tx == nil {
		tx, e = s.db.BeginTx(ctx, nil)
		if e != nil {
			return in, e
		}
		defer tx.Rollback()
	}
	now := time.Now().UTC()
	if e = validateMemoryAssertions(ctx, tx, &in); e != nil {
		return in, e
	}
	// Mention time is derived from user sources only, never model output.
	in.LastMention = time.Time{}
	latest := time.Time{}
	if in.EventYear < 0 || in.EventYear > 9999 {
		return in, errors.New("invalid event year")
	}
	if in.EventTime != "" {
		if _, err := time.Parse(time.RFC3339Nano, in.EventTime); err != nil {
			return in, errors.New("event_time requires an exact source-backed RFC3339 timestamp")
		}
	}
	yearEvidence := in.EventYear == 0
	dateEvidence := in.EventTime == ""
	for _, src := range in.Sources {
		if in.EventYear != 0 && strings.Contains(src.Quote, fmt.Sprint(in.EventYear)) {
			yearEvidence = true
		}
		if in.EventTime != "" {
			stamp, _ := time.Parse(time.RFC3339Nano, in.EventTime)
			if strings.Contains(src.Quote, stamp.Format("2006-01-02")) {
				dateEvidence = true
			}
		}
		var scope, text, role, sender, at string
		var suppressed int
		var payload []byte
		if e = tx.QueryRowContext(ctx, "SELECT scope,text,role,sender,at,suppressed,payload FROM events WHERE id=?", src.EventID).Scan(&scope, &text, &role, &sender, &at, &suppressed, &payload); e != nil {
			return in, e
		}
		if in.Status != "deleted" {
			literal, state := memoryLiteralSource(role, text, payload)
			if state == "unknown" || src.Quote == "" || !strings.Contains(literal, src.Quote) {
				return in, errors.New("memory source does not establish original user words")
			}
		}
		if scope != in.Scope || in.Status != "deleted" && (suppressed != 0 || (role != "user" && !(role == "admin_correction" && len(in.Assertions) > 0)) || src.Quote == "" || !strings.Contains(text, src.Quote) || len(in.Assertions) == 0 && in.Subject != "" && sender != in.Subject) {
			return in, errors.New("memory source scope, author or quote mismatch")
		}
		var blocked int
		if e = tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM memory_blocks WHERE scope=? AND event_id=?)+(SELECT COUNT(*) FROM source_conflicts WHERE event_id=?)", in.Scope, src.EventID, src.EventID).Scan(&blocked); e != nil {
			return in, e
		}
		if blocked > 0 && in.Status != "deleted" {
			return in, errors.New("memory source was explicitly forgotten")
		}
		stamp, _ := time.Parse(time.RFC3339Nano, at)
		if stamp.After(latest) {
			latest = stamp
		}
	}
	if !yearEvidence || !dateEvidence {
		return in, errors.New("event date/year is not present in source quotes")
	}
	if in.ID == "" {
		identity := []any{in.Scope, in.Subject, in.Kind, in.Detail, in.Sources, in.Manual}
		if len(in.Assertions) > 0 {
			identity = append(identity, in.Assertions)
		}
		b, _ := json.Marshal(identity)
		h := sha256.Sum256(b)
		in.ID = hex.EncodeToString(h[:])
	}
	if in.MergeInto != "" {
		var raw []byte
		var existing MemoryEntry
		if e = tx.QueryRowContext(ctx, "SELECT payload FROM memories WHERE id=? AND scope=? AND status='active'", in.MergeInto, in.Scope).Scan(&raw); e != nil {
			return in, e
		}
		if e = json.Unmarshal(raw, &existing); e != nil {
			return in, e
		}
		if existing.Subject != in.Subject || existing.Kind != in.Kind || !sameAssertions(existing.Assertions, in.Assertions) {
			return in, errors.New("duplicate merge must preserve subject and kind")
		}
		replay := true
		for _, src := range in.Sources {
			found := false
			for _, prior := range existing.Sources {
				if src == prior {
					found = true
					break
				}
			}
			if !found {
				replay = false
				break
			}
		}
		if replay {
			return existing, nil
		}
		for i := range in.Assertions {
			for _, src := range existing.Assertions[i].Sources {
				found := false
				for _, cur := range in.Assertions[i].Sources {
					if cur == src {
						found = true
						break
					}
				}
				if !found {
					in.Assertions[i].Sources = append(in.Assertions[i].Sources, src)
				}
			}
		}
		in.ID = existing.ID
		expected = existing.Version
		in.Detail = existing.Detail
		in.Summary = existing.Summary
		in.EventYear = existing.EventYear
		in.EventTime = existing.EventTime
		in.Pinned = existing.Pinned
		in.Continuity = existing.Continuity
		seen := map[string]bool{}
		for _, src := range in.Sources {
			seen[src.EventID] = true
		}
		for _, src := range existing.Sources {
			if !seen[src.EventID] {
				in.Sources = append(in.Sources, src)
			}
		}
		in.MergeInto = ""
	}
	var raw []byte
	var old MemoryEntry
	var storedVersion int
	var storedStatus string
	e = tx.QueryRowContext(ctx, "SELECT payload,version,status FROM memories WHERE id=? AND scope=?", in.ID, in.Scope).Scan(&raw, &storedVersion, &storedStatus)
	if e == nil {
		if e = json.Unmarshal(raw, &old); e != nil {
			return in, e
		}
		old.Version = storedVersion
		old.Status = storedStatus
		// Same immutable candidate is a replay, never a resurrection.
		if expected == 0 {
			var status string
			var version int
			if e = tx.QueryRowContext(ctx, "SELECT status,version FROM memories WHERE id=?", old.ID).Scan(&status, &version); e != nil {
				return in, e
			}
			old.Status = status
			old.Version = version
			return old, nil
		}
		if old.Version != expected {
			return in, ErrMemoryConflict
		}
		if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO memory_revisions VALUES(?,?,?)", old.ID, old.Version, raw); e != nil {
			return in, e
		}
		in.Version = old.Version + 1
		in.Created = old.Created
		in.LastMention = old.LastMention
	} else if !errors.Is(e, sql.ErrNoRows) {
		return in, e
	} else {
		if expected != 0 {
			return in, ErrMemoryConflict
		}
		in.Version = 1
		in.Created = now
	}
	if latest.After(in.LastMention) {
		in.LastMention = latest
	}
	in.Updated = now
	if in.Summary == "" {
		in.Summary = boundedText(in.Detail, 600)
	}
	if in.Inference && in.Status == "active" {
		in.Status = "conflicted"
	}
	for _, conflictID := range in.ConflictsWith {
		var subject, status string
		var raw []byte
		var version int
		if e = tx.QueryRowContext(ctx, "SELECT subject,status,payload,version FROM memories WHERE id=? AND scope=?", conflictID, in.Scope).Scan(&subject, &status, &raw, &version); e != nil {
			return in, e
		}
		if subject != in.Subject || status == "deleted" || status == "superseded" {
			return in, errors.New("conflict target must be a current memory of the same subject")
		}
		if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO memory_revisions VALUES(?,?,?)", conflictID, version, raw); e != nil {
			return in, e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE memories SET status='conflicted',version=version+1 WHERE id=?", conflictID); e != nil {
			return in, e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM embeddings WHERE memory_id=?", conflictID); e != nil {
			return in, e
		}
		in.Status = "conflicted"
	}
	if in.Supersedes != "" {
		var prior MemoryEntry
		var priorRaw []byte
		if e = tx.QueryRowContext(ctx, "SELECT payload FROM memories WHERE id=? AND scope=?", in.Supersedes, in.Scope).Scan(&priorRaw); e != nil {
			return in, e
		}
		if e = json.Unmarshal(priorRaw, &prior); e != nil {
			return in, e
		}
		if prior.Subject != in.Subject || len(in.Sources) == 0 {
			return in, errors.New("correction requires same subject and new source")
		}
		if !in.LastMention.After(prior.LastMention) {
			return in, errors.New("correction must have a newer explicit source")
		}
		prior.Status = "superseded"
		prior.Version++
		b, _ := json.Marshal(prior)
		if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO memory_revisions VALUES(?,?,?)", prior.ID, prior.Version-1, priorRaw); e != nil {
			return in, e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE memories SET status='superseded',version=?,payload=? WHERE id=?", prior.Version, b, prior.ID); e != nil {
			return in, e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM embeddings WHERE memory_id=?", prior.ID); e != nil {
			return in, e
		}
	}
	b, e := json.Marshal(in)
	if e != nil {
		return in, e
	}
	statement := in.LastMention
	if statement.IsZero() {
		statement = now
	}
	eventAt := ""
	if in.EventTime != "" {
		stamp, _ := time.Parse(time.RFC3339Nano, in.EventTime)
		eventAt = memoryTime(stamp)
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO memories(id,scope,subject,kind,status,version,pinned,updated,payload,statement_at,event_at,event_year) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,version=excluded.version,pinned=excluded.pinned,updated=excluded.updated,payload=excluded.payload,statement_at=excluded.statement_at,event_at=excluded.event_at,event_year=excluded.event_year`, in.ID, in.Scope, in.Subject, in.Kind, in.Status, in.Version, in.Pinned, memoryTime(now), b, memoryTime(statement), eventAt, in.EventYear)
	if e != nil {
		return in, e
	}
	for _, q := range []string{"DELETE FROM memory_terms WHERE memory_id=?", "DELETE FROM memory_sources WHERE memory_id=?", "DELETE FROM embeddings WHERE memory_id=?"} {
		if _, e = tx.ExecContext(ctx, q, in.ID); e != nil {
			return in, e
		}
	}
	for _, term := range memoryTerms(in.Detail + " " + strings.Join(in.Keywords, " ")) {
		if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO memory_terms VALUES(?,?,?)", term, in.ID, in.Scope); e != nil {
			return in, e
		}
	}
	for _, src := range in.Sources {
		if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO memory_sources VALUES(?,?,?)", in.ID, src.EventID, src.Quote); e != nil {
			return in, e
		}
	}
	if in.Status == "deleted" {
		for _, src := range in.Sources {
			if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO memory_blocks VALUES(?,?)", in.Scope, src.EventID); e != nil {
				return in, e
			}
		}
		// Legacy summaries/notes have no per-sentence provenance. Invalidate this
		// derived view rather than allow a forgotten source through another path.
		if scope, ok := s.data.Scopes[in.Scope]; ok {
			scope.Summary = ""
			scope.Notes = nil
			scope.CompressedCount = 0
			scope.SummaryBoundary = 0
			raw, _ := json.Marshal(scope)
			if _, e = tx.ExecContext(ctx, "UPDATE scopes SET payload=? WHERE id=?", raw, in.Scope); e != nil {
				return in, e
			}
		}
	}
	if e = bumpMemoryEpoch(tx); e == nil {
		e = bumpRecallEpoch(tx)
	}
	if e == nil && existing == nil {
		e = tx.Commit()
	}
	if e == nil && existing == nil && in.Status == "deleted" {
		scope := s.data.Scopes[in.Scope]
		scope.Summary = ""
		scope.Notes = nil
		scope.CompressedCount = 0
		scope.SummaryBoundary = 0
		s.data.Scopes[in.Scope] = scope
	}
	return in, e
}

func (s *Store) ReadMemory(ctx context.Context, scopes []string, id string) (MemoryEntry, error) {
	var m MemoryEntry
	if len(scopes) == 0 {
		return m, errors.New("memory scope required")
	}
	a := append([]any{id}, stringsArgs(scopes)...)
	var raw []byte
	var status string
	var version int
	e := s.db.QueryRowContext(ctx, "SELECT payload,status,version FROM memories WHERE id=? AND scope IN ("+placeholders(len(scopes))+") AND status IN ('active','conflicted') AND NOT EXISTS(SELECT 1 FROM memory_sources ms JOIN source_conflicts sc ON sc.event_id=ms.event_id WHERE ms.memory_id=memories.id) AND NOT EXISTS(SELECT 1 FROM memory_sources ms JOIN memory_blocks mb ON mb.event_id=ms.event_id AND mb.scope=memories.scope WHERE ms.memory_id=memories.id)"+reliableMemorySourcesSQL("memories"), a...).Scan(&raw, &status, &version)
	if e == nil {
		e = json.Unmarshal(raw, &m)
		m.Status = status
		m.Version = version
	}
	return m, e
}

func (s *Store) MemoryRevisions(ctx context.Context, scopes []string, id string) ([]MemoryEntry, error) {
	if _, err := s.ReadMemory(ctx, scopes, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM memory_revisions WHERE memory_id=? ORDER BY version DESC", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemoryEntry{}
	for rows.Next() {
		var raw []byte
		var m MemoryEntry
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) ReadEvent(ctx context.Context, scopes []string, id string) (MemoryEvent, error) {
	var ev MemoryEvent
	if len(scopes) == 0 {
		return ev, errors.New("memory scope required")
	}
	a := append([]any{id}, stringsArgs(scopes)...)
	var payload []byte
	e := s.db.QueryRowContext(ctx, "SELECT id,scope,trace,role,sender,at,text,hash,complete,revision,seq,payload FROM events WHERE id=? AND scope IN ("+placeholders(len(scopes))+") AND suppressed=0 AND NOT EXISTS(SELECT 1 FROM source_conflicts sc WHERE sc.event_id=events.id) AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=events.scope AND b.event_id=events.id)", a...).Scan(&ev.ID, &ev.Scope, &ev.Trace, &ev.Role, &ev.Sender, &ev.At, &ev.Text, &ev.Hash, &ev.Complete, &ev.Revision, &ev.Seq, &payload)
	if e == nil {
		enrichMemoryEvent(&ev, payload)
		s.redactUnavailableMediaSynthesis(ctx, &ev)
	}
	return ev, e
}

// OriginalEventID resolves a native transport identifier only inside the
// authorized scope and current durable dataset. It never selects a legacy row
// just because a local message number was reused.
func (s *Store) OriginalEventID(ctx context.Context, scope, message string) (string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT e.id FROM events e WHERE e.scope=? AND e.trace=? AND e.role='user' AND e.suppressed=0 AND json_extract(e.payload,'$.source.instance_id')=? AND COALESCE(json_extract(e.payload,'$.source.integrity'),'')!='conflicted' AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=e.scope AND b.event_id=e.id) AND NOT EXISTS(SELECT 1 FROM source_conflicts c WHERE c.event_id=e.id) LIMIT 2", scope, message, s.SourceInstanceID())
	if err != nil {
		return "", err
	}
	defer rows.Close()
	id := ""
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			return "", err
		}
		if id != "" {
			return "", ErrMemoryConflict
		}
		id = value
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if id == "" {
		return "", sql.ErrNoRows
	}
	return id, nil
}

func (s *Store) SearchMemory(ctx context.Context, q MemoryQuery) (MemoryPage, error) {
	p, e := s.searchMemory(ctx, q)
	if e != nil {
		return p, e
	}
	p.CoverageScopes = map[string]string{}
	for _, id := range q.Scopes {
		progress, e := s.ArchiveProgress(ctx, id)
		if e != nil {
			return p, e
		}
		p.CoverageScopes[id] = progress
	}
	return p, nil
}

func (s *Store) searchMemory(ctx context.Context, q MemoryQuery) (MemoryPage, error) {
	p := MemoryPage{Items: []MemoryHit{}, Coverage: "indexed_memory", EffectiveLimit: q.Limit}
	if len(q.Scopes) == 0 || q.Offset < 0 || q.Limit < 0 {
		return p, errors.New("invalid memory query range")
	}
	for _, scope := range q.Scopes {
		if !validScopeID(scope) {
			return p, errors.New("invalid memory scope")
		}
	}
	if q.Limit == 0 {
		q.Limit = 10
	}
	if q.BeforeMessageID != "" || q.MediaKind != "" || q.MetadataOnly || q.Order != "" {
		if !q.Originals {
			return p, errors.New("original message filters require originals=true")
		}
		if q.BeforeMessageID != "" && len(q.Scopes) != 1 {
			return p, errors.New("an original anchor requires one conversation scope")
		}
		if q.Order != "" && q.Order != "time_desc" {
			return p, errors.New("invalid original message order")
		}
		switch q.MediaKind {
		case "", "video", "share", "video_or_share", "image", "voice":
		default:
			return p, errors.New("invalid original media kind")
		}
	}
	if q.EventYear < 0 || q.EventYear > 9999 || q.TimeBasis != "" && q.TimeBasis != "statement" && q.TimeBasis != "event" {
		return p, errors.New("invalid memory event time filter")
	}
	if q.Limit == int(^uint(0)>>1) {
		return p, errors.New("page size exceeds integer range")
	}
	q.Scopes = append([]string{}, q.Scopes...)
	sort.Strings(q.Scopes)
	if q.Cursor != "" && q.Offset != 0 {
		return p, errors.New("cursor and offset are mutually exclusive")
	}
	p.EffectiveLimit = q.Limit
	for _, v := range []string{q.Since, q.Before} {
		if v != "" {
			if _, e := time.Parse(time.RFC3339Nano, v); e != nil {
				return p, errors.New("memory time requires RFC3339 with timezone")
			}
		}
	}
	if q.Since != "" && q.Before != "" {
		a, _ := time.Parse(time.RFC3339Nano, q.Since)
		b, _ := time.Parse(time.RFC3339Nano, q.Before)
		if !a.Before(b) {
			return p, errors.New("since must precede before")
		}
	}
	// Cursor is bound to filters and database commit version: changes ask the
	// caller to restart rather than silently skipping or repeating ranked rows.
	var version string
	if e := s.db.QueryRowContext(ctx, "SELECT COALESCE((SELECT value FROM meta WHERE key='data_epoch'),'0')").Scan(&version); e != nil {
		return p, e
	}
	var maxUpdated string
	_ = s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(updated),'') FROM memories").Scan(&maxUpdated)
	key, _ := json.Marshal([]any{q.Scopes, q.Query, q.Kind, q.Subject, q.Since, q.Before, q.EventYear, q.TimeBasis, q.Originals, q.Space, q.Pinned, q.MinSimilarity, q.BeforeMessageID, q.MediaKind, q.Order, q.MetadataOnly, maxUpdated, version})
	hash := sha256.Sum256(key)
	if q.Cursor != "" {
		var c struct {
			Hash   string
			Offset int
		}
		b, e := base64.RawURLEncoding.DecodeString(q.Cursor)
		if e != nil || json.Unmarshal(b, &c) != nil || c.Hash != hex.EncodeToString(hash[:]) || c.Offset < 0 {
			return p, ErrMemoryConflict
		}
		q.Offset = c.Offset
	}
	terms := memoryTerms(q.Query)
	if q.Originals {
		return s.searchEvents(ctx, q, terms, hash)
	}
	where := "m.scope IN (" + placeholders(len(q.Scopes)) + ") AND m.status IN ('active','conflicted') AND NOT EXISTS(SELECT 1 FROM memory_sources ms JOIN source_conflicts sc ON sc.event_id=ms.event_id WHERE ms.memory_id=m.id) AND NOT EXISTS(SELECT 1 FROM memory_sources ms JOIN memory_blocks mb ON mb.event_id=ms.event_id AND mb.scope=m.scope WHERE ms.memory_id=m.id)"
	where += reliableMemorySourcesSQL("m")
	args := stringsArgs(q.Scopes)
	if q.EventYear > 0 {
		where += " AND m.event_year=?"
		args = append(args, q.EventYear)
	}
	if q.Pinned {
		where += " AND m.pinned=1"
	}
	if q.Kind != "" {
		where += " AND m.kind=?"
		args = append(args, q.Kind)
	}
	if q.Subject != "" {
		where += " AND m.subject=?"
		args = append(args, q.Subject)
	}
	if q.Since != "" {
		column := "m.statement_at"
		if q.TimeBasis == "event" {
			column = "m.event_at"
			where += " AND m.event_at!=''"
		}
		where += " AND " + column + ">=?"
		stamp, _ := time.Parse(time.RFC3339Nano, q.Since)
		args = append(args, memoryTime(stamp))
	}
	if q.Before != "" {
		column := "m.statement_at"
		if q.TimeBasis == "event" {
			column = "m.event_at"
			where += " AND m.event_at!=''"
		}
		where += " AND " + column + "<?"
		stamp, _ := time.Parse(time.RFC3339Nano, q.Before)
		args = append(args, memoryTime(stamp))
	}
	lex := "SELECT m.id,ROW_NUMBER() OVER(ORDER BY m.pinned DESC,m.updated DESC,m.id) rank FROM memories m WHERE " + where
	lexArgs := append([]any{}, args...)
	if len(terms) > 0 {
		lex = "SELECT m.id,ROW_NUMBER() OVER(ORDER BY COUNT(*) DESC,m.id) rank FROM memory_terms t JOIN memories m ON m.id=t.memory_id WHERE " + where + " AND t.term IN (" + placeholders(len(terms)) + ") GROUP BY m.id"
		lexArgs = append(lexArgs, stringsArgs(terms)...)
	}
	query := "WITH lex AS (" + lex + "),scores AS (SELECT id,1.0/(60+rank) score FROM lex) SELECT m.payload,s.score,m.status,m.version FROM scores s JOIN memories m ON m.id=s.id ORDER BY s.score DESC,m.id LIMIT ? OFFSET ?"
	if len(q.Vector) > 0 && q.Space != "" {
		minimum := 0.35
		if q.MinSimilarity != nil {
			minimum = *q.MinSimilarity
		}
		if minimum < -1 || minimum > 1 {
			return p, errors.New("invalid cosine threshold")
		}
		vec := `SELECT m.id,ROW_NUMBER() OVER(ORDER BY memory_cosine(e.vector,?) DESC,m.id) rank FROM embeddings e JOIN memories m ON m.id=e.memory_id WHERE ` + where + " AND e.space=? AND e.version=m.version AND memory_cosine(e.vector,?)>=?"
		vb, _ := json.Marshal(q.Vector)
		all := append([]any{}, lexArgs...)
		all = append(all, string(vb))
		all = append(all, args...)
		all = append(all, q.Space)
		all = append(all, string(vb), minimum)
		lexArgs = all
		query = "WITH lex AS (" + lex + "),vec AS (" + vec + "),scores AS (SELECT id,SUM(score) score FROM (SELECT id,1.0/(60+rank) score FROM lex UNION ALL SELECT id,1.0/(60+rank) score FROM vec) GROUP BY id) SELECT m.payload,s.score,m.status,m.version FROM scores s JOIN memories m ON m.id=s.id ORDER BY s.score DESC,m.id LIMIT ? OFFSET ?"
	} else {
		p.Degraded = "semantic_not_configured_or_unavailable"
	}
	lexArgs = append(lexArgs, q.Limit+1, q.Offset)
	rows, e := s.db.QueryContext(ctx, query, lexArgs...)
	if e != nil {
		return p, e
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		var hit MemoryHit
		var m MemoryEntry
		var status string
		var version int
		if e = rows.Scan(&b, &hit.Score, &status, &version); e != nil {
			return p, e
		}
		if e = json.Unmarshal(b, &m); e != nil {
			return p, e
		}
		m.DetailBytes = len(m.Detail)
		m.DetailExcerpted = m.DetailBytes > 600
		m.Detail = boundedText(m.Detail, 600)
		m.SourceCount = len(m.Sources)
		if len(m.Sources) > 3 {
			m.Sources = m.Sources[len(m.Sources)-3:]
		}
		for i := range m.Sources {
			m.Sources[i].Quote = boundedText(m.Sources[i].Quote, 256)
		}
		m.Status = status
		m.Version = version
		hit.Entry = &m
		hit.Reason = "lexical"
		if len(q.Vector) > 0 {
			hit.Reason = "rank_fusion"
		}
		p.Items = append(p.Items, hit)
	}
	if e = rows.Err(); e != nil {
		return p, e
	}
	finishPage(&p, q, hash)
	return p, nil
}

func finishPage(p *MemoryPage, q MemoryQuery, hash [32]byte) {
	p.HasMore = len(p.Items) > q.Limit
	if p.HasMore {
		p.Items = p.Items[:q.Limit]
	}
	p.NextOffset = q.Offset + len(p.Items)
	if p.HasMore {
		b, _ := json.Marshal(struct {
			Hash   string
			Offset int
		}{hex.EncodeToString(hash[:]), p.NextOffset})
		p.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
}
func (s *Store) searchEvents(ctx context.Context, q MemoryQuery, terms []string, hash [32]byte) (MemoryPage, error) {
	p := MemoryPage{Items: []MemoryHit{}, Coverage: "indexed_originals", EffectiveLimit: q.Limit}
	where := "e.scope IN (" + placeholders(len(q.Scopes)) + ") AND e.suppressed=0 AND NOT EXISTS(SELECT 1 FROM source_conflicts sc WHERE sc.event_id=e.id) AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=e.scope AND b.event_id=e.id)"
	args := stringsArgs(q.Scopes)
	if q.BeforeMessageID != "" {
		// Resolve only an authorized, visible original. Sequence disambiguates
		// equal timestamps; the anchor itself and later replies are excluded.
		var at string
		var seq int64
		a := append(stringsArgs(q.Scopes), q.BeforeMessageID)
		err := s.db.QueryRowContext(ctx, "SELECT e.at,e.seq FROM events e WHERE e.scope IN ("+placeholders(len(q.Scopes))+") AND e.trace=? AND e.role='user' AND e.suppressed=0 AND NOT EXISTS(SELECT 1 FROM source_conflicts sc WHERE sc.event_id=e.id) AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=e.scope AND b.event_id=e.id) ORDER BY e.seq DESC LIMIT 1", a...).Scan(&at, &seq)
		if errors.Is(err, sql.ErrNoRows) {
			return p, ErrOriginalAnchorUnavailable
		}
		if err != nil {
			return p, err
		}
		where += " AND (e.at<? OR (e.at=? AND e.seq<?))"
		args = append(args, at, at, seq)
	}
	if q.MediaKind != "" {
		kinds := []string{q.MediaKind}
		if q.MediaKind == "video_or_share" {
			kinds = []string{"video", "share"}
		}
		where += " AND e.role='user' AND (EXISTS(SELECT 1 FROM json_each(e.payload,'$.incoming_media') im WHERE json_extract(im.value,'$.kind') IN (" + placeholders(len(kinds)) + "))"
		args = append(args, stringsArgs(kinds)...)
		// Older retained sources may predate incoming_media. A native type is
		// still useful directory metadata; type 49 remains only a share
		// candidate until read, never a claim that every share is a video.
		for _, kind := range kinds {
			typ := map[string]int{"video": 43, "share": 49, "image": 3, "voice": 34}[kind]
			where += " OR (json_extract(e.payload,'$.source.message_type')=? AND json_type(e.payload,'$.quoted_source') IS NULL)"
			args = append(args, typ)
		}
		where += ")"
	}
	if q.EventYear > 0 {
		where += " AND instr(e.text,?)>0"
		args = append(args, fmt.Sprint(q.EventYear))
	}
	if q.Subject != "" {
		where += " AND e.sender=?"
		args = append(args, q.Subject)
	}
	for _, b := range []struct{ value, op string }{{q.Since, ">="}, {q.Before, "<"}} {
		if b.value != "" {
			stamp, _ := time.Parse(time.RFC3339Nano, b.value)
			where += " AND e.at" + b.op + "?"
			args = append(args, memoryTime(stamp))
		}
	}
	join := ""
	order := "e.at DESC,e.id"
	if len(terms) > 0 {
		join = " JOIN event_terms t ON t.event_id=e.id"
		where += " AND t.term IN (" + placeholders(len(terms)) + ")"
		args = append(args, stringsArgs(terms)...)
		order = "COUNT(*) DESC,e.at DESC,e.id"
	}
	if q.Order == "time_desc" {
		order = "e.at DESC,e.seq DESC,e.id"
	}
	args = append(args, q.Limit+1, q.Offset)
	rows, e := s.db.QueryContext(ctx, "SELECT e.id,e.scope,e.trace,e.role,e.sender,e.at,e.text,e.hash,e.complete,e.revision,e.seq,e.payload FROM events e"+join+" WHERE "+where+" GROUP BY e.id ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
	if e != nil {
		return p, e
	}
	defer rows.Close()
	for rows.Next() {
		ev := MemoryEvent{}
		var payload []byte
		if e = rows.Scan(&ev.ID, &ev.Scope, &ev.Trace, &ev.Role, &ev.Sender, &ev.At, &ev.Text, &ev.Hash, &ev.Complete, &ev.Revision, &ev.Seq, &payload); e != nil {
			return p, e
		}
		enrichMemoryEvent(&ev, payload)
		ev.TextBytes = len(ev.Text)
		// Search returns previews, just as it does for visual observations.
		// ReadEvent checks source visibility on demand, after this cursor releases
		// the store's sole SQLite connection.
		if ev.Media != nil {
			ev.Media.SynthesisText, ev.Media.SynthesisTextKnown = "", false
		}
		ev.ObservationCount = len(ev.IncomingMedia)
		for _, m := range ev.IncomingMedia {
			ev.ObservationBytes += len(m.Observation)
		}
		ev.Text = boundedText(ev.Text, 600)
		// Search previews never copy complete ASR/OCR/video bodies. Exact
		// observation pages remain available through memory_read.
		for i := range ev.IncomingMedia {
			ev.IncomingMedia[i].Observation = ""
		}
		if q.MetadataOnly {
			ev.Text = ""
			ev.IncomingMedia = nil
			ev.Media = nil
			ev.QuotedSource = nil
			ev.Addressees = nil
			if ev.Source != nil {
				ev.Source = &SourceRef{MessageType: ev.Source.MessageType, Integrity: ev.Source.Integrity}
			}
		}
		p.Items = append(p.Items, MemoryHit{Event: &ev, Reason: "original_source"})
	}
	if e = rows.Err(); e != nil {
		return p, e
	}
	finishPage(&p, q, hash)
	return p, nil
}

// DeliverySynthesisVisible is the single visibility check for a speech draft
// attached to an outgoing artifact. A receipt may remain readable after its
// request is forgotten, but the spoken input may not be recovered through a
// different history or asset route.
func (s *Store) DeliverySynthesisVisible(ctx context.Context, scope, trace, requester string) bool {
	if s == nil || s.db == nil || scope == "" || trace == "" || requester == "" {
		return false
	}
	var visible bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events e WHERE e.scope=? AND e.trace=? AND e.role='user' AND e.sender=? AND e.suppressed=0 AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=e.scope AND b.event_id=e.id) AND NOT EXISTS(SELECT 1 FROM source_conflicts c WHERE c.event_id=e.id))`, scope, trace, requester).Scan(&visible)
	return err == nil && visible
}

func (s *Store) redactUnavailableMediaSynthesis(ctx context.Context, ev *MemoryEvent) {
	if ev == nil || ev.Media == nil || !ev.Media.SynthesisTextKnown {
		return
	}
	m := ev.Media
	if s.DeliverySynthesisVisible(ctx, ev.Scope, m.RequestTraceID, m.RequesterID) {
		return
	}
	copy := *m
	copy.SynthesisText, copy.SynthesisTextKnown = "", false
	ev.Media = &copy
}

type originalAnchorError struct{}

func (originalAnchorError) Error() string { return "original_history_anchor_unavailable" }
func (originalAnchorError) SafeToolFailure() (string, string) {
	return "original_history_anchor_unavailable", "当前授权会话中无法找到或核验锚点原消息；可用wechat_read核对原生历史和覆盖范围，不能据此断言全部历史不存在"
}

var ErrOriginalAnchorUnavailable error = originalAnchorError{}

func (m MemoryEntry) Representation(now time.Time) string {
	if m.Pinned || now.Sub(m.LastMention) < 72*time.Hour {
		return "detail"
	}
	if now.Sub(m.LastMention) >= 14*24*time.Hour {
		return "cold"
	}
	return "summary"
}

// Stable ordering makes equal-score pages and identical replay fixtures reproducible.
func sortMemories(v []MemoryEntry) { sort.Slice(v, func(i, j int) bool { return v[i].ID < v[j].ID }) }

func memoryQuoteError(id string) error { return fmt.Errorf("memory source unavailable: %s", id) }
