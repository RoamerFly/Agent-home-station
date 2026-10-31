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

// IndexedDocument is a rebuildable projection. The task/media stores continue
// to own status, originals and receipts; this index never changes those stores.
type IndexedDocument struct {
	ID, Scope, Kind, Text string
	At                    time.Time
	Data                  map[string]any
	Evidence              *DocumentEvidence
}

// DocumentEvidence separates facts from cache bookkeeping. Nil protects every
// query-visible field. Explicit producers must include all factual fields in
// Stable; scope, kind and search text are always protected. AppendOnly records
// may grow without invalidation, but changing/removing old records invalidates.
type DocumentEvidence struct {
	Stable     map[string]any
	AppendOnly map[string][]any
}

type documentEvidenceState struct {
	StableHash string              `json:"stable_hash"`
	AppendOnly map[string][]string `json:"append_only,omitempty"`
}

func documentEvidenceSnapshot(d IndexedDocument, fullHash string) (documentEvidenceState, error) {
	state := documentEvidenceState{StableHash: fullHash}
	if d.Evidence == nil {
		return state, nil
	}
	b, err := json.Marshal([]any{"explicit_evidence_v1", d.Scope, d.Kind, d.Text, d.Evidence.Stable})
	if err != nil {
		return state, err
	}
	h := sha256.Sum256(b)
	state.StableHash = hex.EncodeToString(h[:])
	state.AppendOnly = make(map[string][]string, len(d.Evidence.AppendOnly))
	for key, records := range d.Evidence.AppendOnly {
		hashes := make([]string, 0, len(records))
		for _, record := range records {
			b, err := json.Marshal(record)
			if err != nil {
				return state, err
			}
			h := sha256.Sum256(b)
			hashes = append(hashes, hex.EncodeToString(h[:]))
		}
		state.AppendOnly[key] = hashes
	}
	return state, nil
}

func documentEvidenceInvalidated(old, next documentEvidenceState) bool {
	if old.StableHash != next.StableHash || len(old.AppendOnly) != len(next.AppendOnly) {
		return true
	}
	for key, hashes := range old.AppendOnly {
		current, ok := next.AppendOnly[key]
		if !ok || len(current) < len(hashes) {
			return true
		}
		for i, hash := range hashes {
			if current[i] != hash {
				return true
			}
		}
	}
	return false
}

func (s *Store) SyncDocuments(ctx context.Context, namespace string, docs []IndexedDocument) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	changed, invalidated := false, false
	namespaceHash := sha256.Sum256([]byte(namespace))
	evidencePrefix := "document_evidence:" + hex.EncodeToString(namespaceHash[:]) + ":"
	for _, d := range docs {
		b, e := json.Marshal(d.Data)
		if e != nil {
			return e
		}
		// Every query-visible field belongs to the projection identity. A text
		// correction with unchanged auxiliary Data must not keep the old body.
		identity, e := json.Marshal([]any{d.Scope, d.Kind, memoryTime(d.At), d.Text, d.Data})
		if e != nil {
			return e
		}
		hash := sha256.Sum256(identity)
		digest := hex.EncodeToString(hash[:])
		var previous string
		readErr := tx.QueryRowContext(ctx, "SELECT hash FROM search_documents WHERE namespace=? AND id=?", namespace, d.ID).Scan(&previous)
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return readErr
		}
		nextEvidence, e := documentEvidenceSnapshot(d, digest)
		if e != nil {
			return e
		}
		evidenceKey := evidencePrefix + hex.EncodeToString([]byte(d.ID))
		if readErr == nil {
			var raw string
			metaErr := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", evidenceKey).Scan(&raw)
			if errors.Is(metaErr, sql.ErrNoRows) {
				// An older binary stored only the full projection hash. Explicit
				// evidence policies become trustworthy after one conservative bump.
				invalidated = invalidated || previous != digest || d.Evidence != nil
			} else if metaErr != nil {
				return metaErr
			} else {
				var priorEvidence documentEvidenceState
				if e := json.Unmarshal([]byte(raw), &priorEvidence); e != nil {
					return e
				}
				invalidated = invalidated || documentEvidenceInvalidated(priorEvidence, nextEvidence)
			}
		}
		if errors.Is(readErr, sql.ErrNoRows) || previous != digest {
			changed = true
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO search_documents(namespace,id,scope,kind,at,text,payload,hash) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(namespace,id) DO UPDATE SET scope=excluded.scope,kind=excluded.kind,at=excluded.at,text=excluded.text,payload=excluded.payload,hash=excluded.hash WHERE search_documents.hash!=excluded.hash`, namespace, d.ID, d.Scope, d.Kind, memoryTime(d.At), d.Text, b, digest); e != nil {
			return e
		}
		snapshot, e := json.Marshal(nextEvidence)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE value!=excluded.value", evidenceKey, string(snapshot)); e != nil {
			return e
		}
	}
	// Existing source records are immutable identifiers. A missing record in the
	// authoritative snapshot invalidates its projection in the same transaction.
	if _, e = tx.ExecContext(ctx, "CREATE TEMP TABLE IF NOT EXISTS document_snapshot(id TEXT PRIMARY KEY)"); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM document_snapshot"); e != nil {
		return e
	}
	for _, d := range docs {
		if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO document_snapshot VALUES(?)", d.ID); e != nil {
			return e
		}
	}
	removed, e := tx.ExecContext(ctx, "DELETE FROM search_documents WHERE namespace=? AND id NOT IN (SELECT id FROM document_snapshot)", namespace)
	if e != nil {
		return e
	}
	n, e := removed.RowsAffected()
	if e != nil {
		return e
	}
	if n > 0 {
		changed, invalidated = true, true
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM meta WHERE key GLOB ? AND key NOT IN(SELECT ? || lower(hex(id)) FROM document_snapshot)", evidencePrefix+"*", evidencePrefix); e != nil {
		return e
	}
	if changed {
		e = bumpMemoryEpoch(tx)
	}
	if e != nil {
		return e
	}
	if invalidated {
		if e = bumpRecallEpoch(tx); e != nil {
			return e
		}
	}
	return tx.Commit()
}

type GlobalQuery struct {
	Labels                              map[string]string
	Query, Since, Before, Order, Cursor string
	Offset, Limit                       int
	Archives                            bool
}
type GlobalPage struct {
	Items          []map[string]any  `json:"items"`
	Total          int               `json:"total"`
	NextOffset     int               `json:"next_offset"`
	NextCursor     string            `json:"next_cursor,omitempty"`
	HasMore        bool              `json:"has_more"`
	EffectiveLimit int               `json:"effective_limit"`
	Coverage       map[string]string `json:"coverage"`
	Degraded       string            `json:"degraded"`
}

func (s *Store) SearchGlobal(ctx context.Context, q GlobalQuery) (GlobalPage, error) {
	p := GlobalPage{Items: []map[string]any{}, Coverage: map[string]string{}, Degraded: "lexical_metadata_only"}
	if q.Limit == 0 {
		q.Limit = 30
	}
	p.EffectiveLimit = q.Limit
	if q.Limit < 1 || q.Offset < 0 || q.Limit == int(^uint(0)>>1) || q.Order != "" && q.Order != "time" && q.Order != "relevance" {
		return p, errors.New("invalid global search range or order")
	}
	ids := []string{}
	for id := range q.Labels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return p, nil
	}
	var epoch string
	if e := s.db.QueryRowContext(ctx, "SELECT COALESCE((SELECT value FROM meta WHERE key='data_epoch'),'0')").Scan(&epoch); e != nil {
		return p, e
	}
	key, _ := json.Marshal([]any{q.Labels, q.Query, q.Since, q.Before, q.Order, q.Archives, epoch})
	hash := sha256.Sum256(key)
	mq := MemoryQuery{Offset: q.Offset, Limit: q.Limit}
	if q.Cursor != "" {
		if q.Offset != 0 {
			return p, errors.New("cursor and offset are mutually exclusive")
		}
		var c struct {
			Hash   string
			Offset int
		}
		b, e := base64.RawURLEncoding.DecodeString(q.Cursor)
		if e != nil || json.Unmarshal(b, &c) != nil || c.Hash != hex.EncodeToString(hash[:]) || c.Offset < 0 {
			return p, ErrMemoryConflict
		}
		mq.Offset = c.Offset
	}
	values := []string{}
	args := []any{}
	for _, id := range ids {
		values = append(values, "(?,?)")
		args = append(args, id, q.Labels[id])
		status, e := s.ArchiveProgress(ctx, id)
		if e != nil {
			return p, e
		}
		p.Coverage[id] = status
	}
	active := " AND e.active=1"
	if q.Archives {
		active = ""
	}
	cte := `WITH labels(scope,label) AS (VALUES ` + strings.Join(values, ",") + `),records AS (
 SELECT e.id key,e.scope,'message' kind,e.at,e.text,e.trace,e.sender,e.role,e.complete,e.payload FROM events e WHERE e.suppressed=0` + active + ` AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=e.scope AND b.event_id=e.id)
 UNION ALL SELECT d.id,d.scope,d.kind,d.at,d.text,'','','',1,d.payload FROM search_documents d
 UNION ALL SELECT s.id,s.id,'summary',memory_time(json_extract(s.payload,'$.summary_updated')),json_extract(s.payload,'$.summary'),'','','',0,s.payload FROM scopes s WHERE COALESCE(json_extract(s.payload,'$.summary'),'')!=''
 ),selected AS (SELECT r.*,l.label FROM records r JOIN labels l ON l.scope=r.scope WHERE 1=1`
	if q.Query != "" {
		if q.Order == "relevance" {
			terms := memoryTerms(q.Query)
			parts := []string{}
			for _, term := range terms {
				parts = append(parts, "instr(lower(r.text||l.label||r.key||r.trace),?)>0")
				args = append(args, term)
			}
			if len(parts) > 0 {
				cte += " AND (" + strings.Join(parts, " OR ") + ")"
			}
		} else {
			cte += " AND instr(lower(r.text||l.label||r.key||r.trace),?)>0"
			args = append(args, strings.ToLower(q.Query))
		}
	}
	for _, v := range []struct{ raw, op string }{{q.Since, ">="}, {q.Before, "<"}} {
		if v.raw != "" {
			stamp, e := time.Parse(time.RFC3339Nano, v.raw)
			if e != nil {
				return p, e
			}
			cte += " AND r.at" + v.op + "?"
			args = append(args, memoryTime(stamp))
		}
	}
	cte += ") "
	if e := s.db.QueryRowContext(ctx, cte+"SELECT COUNT(*) FROM selected", args...).Scan(&p.Total); e != nil {
		return p, e
	}
	order := "at DESC,scope,kind,key"
	if q.Order == "relevance" && q.Query != "" {
		scores := []string{}
		for _, term := range memoryTerms(q.Query) {
			scores = append(scores, "(instr(lower(text||label||key||trace),?)>0)")
			args = append(args, term)
		}
		if len(scores) > 0 {
			order = "(" + strings.Join(scores, "+") + ") DESC," + order
		}
	}
	args = append(args, q.Limit+1, mq.Offset)
	rows, e := s.db.QueryContext(ctx, cte+"SELECT key,scope,kind,at,text,trace,sender,role,complete,payload,label FROM selected ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
	if e != nil {
		return p, e
	}
	for rows.Next() {
		var id, scope, kind, at, text, trace, sender, role, label string
		var complete bool
		var raw []byte
		if e = rows.Scan(&id, &scope, &kind, &at, &text, &trace, &sender, &role, &complete, &raw, &label); e != nil {
			break
		}
		data := map[string]any{}
		if kind == "asset" {
			if e = json.Unmarshal(raw, &data); e != nil {
				break
			}
		} else if kind == "message" {
			var t Turn
			_ = json.Unmarshal(raw, &t)
			data = map[string]any{"message_id": trace, "sender": sender, "sender_name": t.SenderName, "media": t.Media, "role": role, "text": boundedText(text, 2000), "full_text_bytes": len(text), "text_excerpted": len(text) > 2000, "source_event_id": id, "complete": complete, "time_basis": "message"}
		} else {
			data = map[string]any{"text": text, "time_basis": "summary_updated"}
		}
		data["at"] = at
		data["kind"] = kind
		data["scope"] = scope
		data["thread_name"] = label
		p.Items = append(p.Items, data)
	}
	rowErr := rows.Err()
	rows.Close()
	if e != nil {
		return p, e
	}
	if rowErr != nil {
		return p, rowErr
	}
	if mq.Offset > p.Total {
		return p, fmt.Errorf("offset exceeds %d results", p.Total)
	}
	page := MemoryPage{Items: make([]MemoryHit, len(p.Items))}
	finishPage(&page, mq, hash)
	p.HasMore = page.HasMore
	p.NextOffset = page.NextOffset
	p.NextCursor = page.NextCursor
	if p.HasMore {
		p.Items = p.Items[:q.Limit]
	}
	return p, nil
}
