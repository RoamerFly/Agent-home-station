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
	Continuity  bool           `json:"continuity,omitempty"`
	MergeInto   string         `json:"merge_into,omitempty"`
	ID          string         `json:"id"`
	Scope       string         `json:"scope"`
	Subject     string         `json:"subject"`
	Kind        string         `json:"kind"`
	Detail      string         `json:"detail"`
	Summary     string         `json:"summary"`
	Keywords    []string       `json:"keywords"`
	Sources     []MemorySource `json:"sources"`
	Status      string         `json:"status"`
	Version     int            `json:"version"`
	Pinned      bool           `json:"pinned"`
	Inference   bool           `json:"inference"`
	Manual      bool           `json:"manual"`
	Supersedes  string         `json:"supersedes,omitempty"`
	EventTime   string         `json:"event_time,omitempty"`
	Created     time.Time      `json:"created"`
	Updated     time.Time      `json:"updated"`
	LastMention time.Time      `json:"last_mention"`
}
type MemoryHit struct {
	Entry  *MemoryEntry `json:"entry,omitempty"`
	Event  *MemoryEvent `json:"event,omitempty"`
	Reason string       `json:"reason"`
	Score  float64      `json:"score"`
}
type MemoryEvent struct {
	SenderName string       `json:"sender_name,omitempty"`
	Media      *MediaMemory `json:"media,omitempty"`
	ID         string       `json:"id"`
	Scope      string       `json:"scope"`
	Trace      string       `json:"message_id"`
	Role       string       `json:"role"`
	Sender     string       `json:"sender"`
	At         string       `json:"at"`
	Text       string       `json:"text"`
	Hash       string       `json:"hash"`
	Complete   bool         `json:"complete"`
	Revision   int          `json:"revision"`
	Seq        int64        `json:"seq"`
}
type MemoryQuery struct {
	MinSimilarity *float64  `json:"min_similarity,omitempty"`
	Pinned        bool      `json:"pinned,omitempty"`
	Scopes        []string  `json:"-"`
	Query         string    `json:"query"`
	Kind          string    `json:"kind,omitempty"`
	Subject       string    `json:"subject,omitempty"`
	Since         string    `json:"since,omitempty"`
	Before        string    `json:"before,omitempty"`
	Cursor        string    `json:"cursor,omitempty"`
	Offset        int       `json:"offset,omitempty"`
	Limit         int       `json:"limit,omitempty"`
	Originals     bool      `json:"originals,omitempty"`
	Vector        []float32 `json:"-"`
	Space         string    `json:"-"`
}
type MemoryPage struct {
	Items          []MemoryHit `json:"items"`
	NextCursor     string      `json:"next_cursor,omitempty"`
	NextOffset     int         `json:"next_offset"`
	HasMore        bool        `json:"has_more"`
	Coverage       string      `json:"coverage"`
	Degraded       string      `json:"degraded,omitempty"`
	EffectiveLimit int         `json:"effective_limit"`
}

var ErrMemoryConflict = errors.New("memory revision changed; read current revision before updating")

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
	s.mu.Lock()
	defer s.mu.Unlock()
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
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return in, e
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	latest := time.Time{}
	for _, src := range in.Sources {
		var scope, text, role, sender, at string
		var suppressed int
		if e = tx.QueryRowContext(ctx, "SELECT scope,text,role,sender,at,suppressed FROM events WHERE id=?", src.EventID).Scan(&scope, &text, &role, &sender, &at, &suppressed); e != nil {
			return in, e
		}
		if scope != in.Scope || suppressed != 0 || role != "user" || src.Quote == "" || !strings.Contains(text, src.Quote) || in.Subject != "" && sender != in.Subject {
			return in, errors.New("memory source scope, author or quote mismatch")
		}
		var blocked int
		if e = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_blocks WHERE scope=? AND event_id=?", in.Scope, src.EventID).Scan(&blocked); e != nil {
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
	if in.ID == "" {
		b, _ := json.Marshal([]any{in.Scope, in.Subject, in.Kind, in.Detail, in.Sources, in.Manual})
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
		if existing.Subject != in.Subject || existing.Kind != in.Kind {
			return in, errors.New("duplicate merge must preserve subject and kind")
		}
		in.ID = existing.ID
		expected = existing.Version
		in.Detail = existing.Detail
		in.Summary = existing.Summary
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
	_, e = tx.ExecContext(ctx, `INSERT INTO memories VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,version=excluded.version,pinned=excluded.pinned,updated=excluded.updated,payload=excluded.payload`, in.ID, in.Scope, in.Subject, in.Kind, in.Status, in.Version, in.Pinned, now.Format(time.RFC3339Nano), b)
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
	}
	if e = bumpMemoryEpoch(tx); e == nil {
		e = tx.Commit()
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
	e := s.db.QueryRowContext(ctx, "SELECT payload,status,version FROM memories WHERE id=? AND scope IN ("+placeholders(len(scopes))+") AND status IN ('active','conflicted') AND NOT EXISTS(SELECT 1 FROM memory_sources ms JOIN memory_blocks mb ON mb.event_id=ms.event_id AND mb.scope=memories.scope WHERE ms.memory_id=memories.id)", a...).Scan(&raw, &status, &version)
	if e == nil {
		e = json.Unmarshal(raw, &m)
		m.Status = status
		m.Version = version
	}
	return m, e
}
func (s *Store) ReadEvent(ctx context.Context, scopes []string, id string) (MemoryEvent, error) {
	var ev MemoryEvent
	if len(scopes) == 0 {
		return ev, errors.New("memory scope required")
	}
	a := append([]any{id}, stringsArgs(scopes)...)
	e := s.db.QueryRowContext(ctx, "SELECT id,scope,trace,role,sender,at,text,hash,complete,revision,seq FROM events WHERE id=? AND scope IN ("+placeholders(len(scopes))+") AND suppressed=0 AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=events.scope AND b.event_id=events.id)", a...).Scan(&ev.ID, &ev.Scope, &ev.Trace, &ev.Role, &ev.Sender, &ev.At, &ev.Text, &ev.Hash, &ev.Complete, &ev.Revision, &ev.Seq)
	return ev, e
}

func (s *Store) SearchMemory(ctx context.Context, q MemoryQuery) (MemoryPage, error) {
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
	// Cursor is bound to filters and database commit version: changes ask the
	// caller to restart rather than silently skipping or repeating ranked rows.
	var version string
	if e := s.db.QueryRowContext(ctx, "SELECT COALESCE((SELECT value FROM meta WHERE key='data_epoch'),'0')").Scan(&version); e != nil {
		return p, e
	}
	var maxUpdated string
	_ = s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(updated),'') FROM memories").Scan(&maxUpdated)
	key, _ := json.Marshal([]any{q.Scopes, q.Query, q.Kind, q.Subject, q.Since, q.Before, q.Originals, q.Space, q.Pinned, q.MinSimilarity, maxUpdated, version})
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
	where := "m.scope IN (" + placeholders(len(q.Scopes)) + ") AND m.status IN ('active','conflicted') AND NOT EXISTS(SELECT 1 FROM memory_sources ms JOIN memory_blocks mb ON mb.event_id=ms.event_id AND mb.scope=m.scope WHERE ms.memory_id=m.id)"
	args := stringsArgs(q.Scopes)
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
		where += " AND m.updated>=?"
		args = append(args, q.Since)
	}
	if q.Before != "" {
		where += " AND m.updated<?"
		args = append(args, q.Before)
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
	where := "e.scope IN (" + placeholders(len(q.Scopes)) + ") AND e.suppressed=0 AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=e.scope AND b.event_id=e.id)"
	args := stringsArgs(q.Scopes)
	if q.Subject != "" {
		where += " AND e.sender=?"
		args = append(args, q.Subject)
	}
	for _, b := range []struct{ value, op string }{{q.Since, ">="}, {q.Before, "<"}} {
		if b.value != "" {
			stamp, _ := time.Parse(time.RFC3339Nano, b.value)
			where += " AND e.at" + b.op + "?"
			args = append(args, stamp.UTC().Format(time.RFC3339Nano))
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
	args = append(args, q.Limit+1, q.Offset)
	rows, e := s.db.QueryContext(ctx, "SELECT e.id,e.scope,e.trace,e.role,e.sender,e.at,e.text,e.hash,e.complete,e.revision,e.seq FROM events e"+join+" WHERE "+where+" GROUP BY e.id ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
	if e != nil {
		return p, e
	}
	defer rows.Close()
	for rows.Next() {
		ev := MemoryEvent{}
		if e = rows.Scan(&ev.ID, &ev.Scope, &ev.Trace, &ev.Role, &ev.Sender, &ev.At, &ev.Text, &ev.Hash, &ev.Complete, &ev.Revision, &ev.Seq); e != nil {
			return p, e
		}
		ev.Text = boundedText(ev.Text, 600)
		p.Items = append(p.Items, MemoryHit{Event: &ev, Reason: "original_source"})
	}
	if e = rows.Err(); e != nil {
		return p, e
	}
	finishPage(&p, q, hash)
	return p, nil
}

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
