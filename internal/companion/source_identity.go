package companion

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"
)

func (s *Store) initializeSourceInstance() error {
	var id string
	err := s.db.QueryRow("SELECT value FROM meta WHERE key='source_instance'").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var b [24]byte
		if _, err = rand.Read(b[:]); err != nil {
			return err
		}
		id = hex.EncodeToString(b[:])
		if _, err = s.db.Exec("INSERT OR IGNORE INTO meta(key,value) VALUES('source_instance',?)", id); err != nil {
			return err
		}
		if err = s.db.QueryRow("SELECT value FROM meta WHERE key='source_instance'").Scan(&id); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if len(id) != 48 {
		return errors.New("invalid persisted source instance")
	}
	s.sourceInstance = id
	return nil
}

// SourceInstanceID is fixed for the durable store, including copied snapshots.
func (s *Store) SourceInstanceID() string { return s.sourceInstance }
func cloneSource(v *SourceRef) *SourceRef {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}
func cloneReplyContext(v *ReplyContext) *ReplyContext {
	if v == nil {
		return nil
	}
	c := *v
	if v.SourceVersions != nil {
		c.SourceVersions = make(map[string]int, len(v.SourceVersions))
		for id, rev := range v.SourceVersions {
			c.SourceVersions[id] = rev
		}
	}
	c.TargetIDs = append([]string(nil), v.TargetIDs...)
	c.SourceEventIDs = append([]string(nil), v.SourceEventIDs...)
	c.MediaSourceIDs = append([]string(nil), v.MediaSourceIDs...)
	c.RiskFlags = append([]string(nil), v.RiskFlags...)
	return &c
}
func cloneIncomingMedia(v []IncomingMediaRef) []IncomingMediaRef {
	return append([]IncomingMediaRef(nil), v...)
}
func cloneTurnProvenance(t *Turn) {
	t.Source = cloneSource(t.Source)
	t.QuotedSource = cloneSource(t.QuotedSource)
	t.ReplyContext = cloneReplyContext(t.ReplyContext)
	t.Addressees = append([]string(nil), t.Addressees...)
	t.IncomingMedia = cloneIncomingMedia(t.IncomingMedia)
}
func sourceHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}
func sourceTurnID(scope string, t Turn) string {
	r := t.Source
	key := scope + "\x00" + t.Role + "\x00" + r.InstanceID + "\x00" + r.MessageID + "\x00" + r.ServerID + "\x00" + r.Sender + "\x00" + r.OccurredAt
	if r.Integrity == "conflicted" {
		key += "\x00" + r.BodyHash
	}
	return sourceHash(key)
}
func sameSourceInstance(a, b *SourceRef) bool {
	// A legacy record without a dataset identity is not a wildcard for every
	// native dataset. Local row IDs can be reused after importing old history.
	// Keep unknown legacy records separate; never infer a current namespace.
	instance := func(r *SourceRef) string {
		if r == nil {
			return ""
		}
		return r.InstanceID
	}
	return instance(a) == instance(b)
}
func legacyConflictSource(in Input, body string, now time.Time, instance string) *SourceRef {
	return &SourceRef{InstanceID: instance, MessageID: in.TraceID, Account: in.Account, Thread: in.Thread, Sender: in.Sender, OccurredAt: in.MessageTime, ReceivedAt: memoryTime(now), BodyHash: sourceHash(body), Revision: 1, Integrity: "conflicted"}
}
func (s *Store) normalizeSource(in Input, body string, now time.Time) (*SourceRef, error) {
	r := cloneSource(in.Source)
	if r == nil {
		return nil, nil
	}
	if r.Account != "" && r.Account != in.Account || r.Thread != "" && r.Thread != in.Thread || r.Sender != "" && r.Sender != in.Sender {
		return nil, errors.New("source identity does not match input")
	}
	if r.InstanceID == "" {
		r.InstanceID = s.SourceInstanceID()
	}
	if r.MessageID == "" {
		r.MessageID = in.TraceID
	}
	r.Account = in.Account
	r.Thread = in.Thread
	r.Sender = in.Sender
	if r.OccurredAt == "" {
		r.OccurredAt = in.MessageTime
	}
	if r.OccurredAt != "" {
		at, e := time.Parse(time.RFC3339Nano, r.OccurredAt)
		if e != nil {
			return nil, errors.New("invalid source occurrence time")
		}
		if in.MessageTime != "" {
			stamp, err := time.Parse(time.RFC3339Nano, in.MessageTime)
			if err != nil || !stamp.Equal(at) {
				return nil, errors.New("source occurrence time does not match input")
			}
		}
		r.OccurredAt = memoryTime(at)
	}
	r.ReceivedAt = memoryTime(now)
	if r.Revision < 1 {
		r.Revision = 1
	}
	r.BodyHash = sourceHash(body)
	if r.Integrity == "" {
		r.Integrity = "observed"
	}
	r.EventID = ""
	return r, nil
}
func sourceCollision(t Turn, r *SourceRef, body, stamp string) bool {
	if r == nil {
		if stamp == "" {
			return false
		}
		at, e := time.Parse(time.RFC3339Nano, stamp)
		return e == nil && !at.Equal(t.Time)
	}
	old := t.Source
	if old == nil {
		return r.Sender != t.Sender || r.OccurredAt != "" && r.OccurredAt != memoryTime(t.Time)
	}
	if old.Integrity == "conflicted" {
		return true
	}
	if old.ServerID != r.ServerID || old.Sender != r.Sender || old.OccurredAt != r.OccurredAt {
		return true
	}
	if old.BodyHash != r.BodyHash {
		return r.Revision <= old.Revision
	}
	return false
}
func markSourceConflict(t *Turn) {
	if t.Source == nil {
		t.Source = &SourceRef{EventID: t.EventID, MessageID: t.TraceID, Sender: t.Sender, OccurredAt: memoryTime(t.Time), BodyHash: sourceHash(t.FullText), Revision: 1}
	} else {
		t.Source = cloneSource(t.Source)
	}
	t.Source.Integrity = "conflicted"
}

// SourceAudit exposes identity metadata only and opens an existing database read-only.
type SourceAudit struct {
	EventID  string `json:"event_id"`
	Scope    string `json:"scope"`
	Trace    string `json:"message_id"`
	Revision int    `json:"revision"`
	Hash     string `json:"hash"`
	Reason   string `json:"reason"`
}

func AuditSources(ctx context.Context, dir string) ([]SourceAudit, error) {
	db, e := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "memory.sqlite"))+"?mode=ro")
	if e != nil {
		return nil, e
	}
	defer db.Close()
	rows, e := db.QueryContext(ctx, "SELECT id,scope,trace,at,sender,revision,hash,payload FROM events ORDER BY scope,trace,at")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []SourceAudit{}
	for rows.Next() {
		var id, scope, trace, at, sender, hash string
		var rev int
		var raw []byte
		if e = rows.Scan(&id, &scope, &trace, &at, &sender, &rev, &hash, &raw); e != nil {
			return nil, e
		}
		var t Turn
		if e = json.Unmarshal(raw, &t); e != nil {
			return nil, e
		}
		reason := ""
		if at != memoryTime(t.Time) || sender != t.Sender {
			reason = "event_projection_identity_mismatch"
		}
		if t.Source != nil && t.Source.Integrity == "conflicted" {
			reason = "source_identity_conflict"
		}
		if reason != "" {
			out = append(out, SourceAudit{id, scope, trace, rev, hash, reason})
		}
	}
	return out, rows.Err()
}

// QuarantineSources requires exact reviewed versions and hashes. It preserves
// raw events, clears affected derived state and returns atomically on failure.
func (s *Store) QuarantineSources(ctx context.Context, items []SourceAudit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	changed := map[string]Scope{}
	for _, item := range items {
		var hash, scope string
		var rev int
		var raw []byte
		if e = tx.QueryRowContext(ctx, "SELECT scope,revision,hash,payload FROM events WHERE id=?", item.EventID).Scan(&scope, &rev, &hash, &raw); e != nil {
			return e
		}
		if scope != item.Scope || rev != item.Revision || hash != item.Hash {
			return errors.New("source quarantine revision conflict")
		}
		var turn Turn
		if e = json.Unmarshal(raw, &turn); e != nil {
			return e
		}
		markSourceConflict(&turn)
		b, _ := json.Marshal(turn)
		for _, q := range []string{"UPDATE events SET payload=? WHERE id=?"} {
			if _, e = tx.ExecContext(ctx, q, b, item.EventID); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO source_conflicts VALUES(?,?,?,?)", scope, item.EventID, "", "reviewed_source_identity_conflict"); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE memories SET status='conflicted',version=version+1 WHERE id IN(SELECT memory_id FROM memory_sources WHERE event_id=?) AND status='active'", item.EventID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM embeddings WHERE memory_id IN(SELECT memory_id FROM memory_sources WHERE event_id=?)", item.EventID); e != nil {
			return e
		}
		sc, ok := changed[scope]
		if !ok {
			sc = copyScope(s.data.Scopes[scope])
		}
		for i := range sc.Messages {
			if sc.Messages[i].EventID == item.EventID {
				markSourceConflict(&sc.Messages[i])
			}
		}
		sc.Summary = ""
		sc.CompressedCount = 0
		sc.SummaryBoundary = 0
		changed[scope] = sc
	}
	for id, sc := range changed {
		raw, _ := json.Marshal(sc)
		if _, e = tx.ExecContext(ctx, "UPDATE scopes SET payload=? WHERE id=?", raw, id); e != nil {
			return e
		}
	}
	if e = bumpMemoryEpoch(tx); e != nil {
		return e
	}
	if len(items) > 0 {
		if e = bumpRecallEpoch(tx); e != nil {
			return e
		}
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	for id, sc := range changed {
		s.data.Scopes[id] = sc
	}
	return nil
}

func sourceReplay(a, b *SourceRef) bool {
	return a != nil && b != nil && a.InstanceID == b.InstanceID && a.MessageID == b.MessageID && a.ServerID == b.ServerID && a.Sender == b.Sender && a.OccurredAt == b.OccurredAt && a.BodyHash == b.BodyHash && a.Revision == b.Revision && a.Integrity == "conflicted"
}

// SourceForInput resolves the exact observed event without using adjacent text.
func (s *Store) SourceForInput(in Input) (*SourceRef, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, false
	}
	scope, ok := s.data.Scopes[ScopeID(in.Channel, in.Account, in.Thread)]
	if !ok {
		return nil, false
	}
	for _, t := range scope.Messages {
		if t.Role != "user" || (t.TraceID != in.TraceID && !sameSourceMessage(t.Source, in.Source)) || t.Sender != in.Sender || t.Source == nil || t.Source.Integrity == "conflicted" {
			continue
		}
		if in.Source != nil {
			if in.Source.InstanceID != "" && in.Source.InstanceID != t.Source.InstanceID || in.Source.ServerID != "" && in.Source.ServerID != t.Source.ServerID ||
				in.Source.EventID != "" && in.Source.EventID != t.EventID || in.Source.MessageID != "" && in.Source.MessageID != t.Source.MessageID ||
				in.Source.Account != "" && in.Source.Account != in.Account || in.Source.Thread != "" && in.Source.Thread != in.Thread ||
				in.Source.Sender != "" && in.Source.Sender != t.Sender {
				continue
			}
			if in.Source.OccurredAt != "" {
				stamp, err := time.Parse(time.RFC3339Nano, in.Source.OccurredAt)
				if err != nil || !stamp.Equal(t.Time) {
					continue
				}
			}
		}
		if in.MessageTime != "" {
			stamp, e := time.Parse(time.RFC3339Nano, in.MessageTime)
			if e != nil || !stamp.Equal(t.Time) {
				continue
			}
		}
		var revision int
		var hash string
		err := s.db.QueryRow("SELECT e.revision,e.hash FROM events e WHERE e.id=? AND e.scope=? AND e.suppressed=0 AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=e.scope AND b.event_id=e.id) AND NOT EXISTS(SELECT 1 FROM source_conflicts c WHERE c.event_id=e.id)", t.EventID, scope.ID).Scan(&revision, &hash)
		if err != nil || t.Source.Revision != revision || t.Source.BodyHash != hash {
			continue
		}
		if in.Source != nil {
			if in.Source.Revision > 0 && in.Source.Revision != revision {
				continue
			}
			// EventID distinguishes a persisted canonical reference from a new
			// native packet, whose BodyHash can still describe raw media XML.
			if in.Source.EventID != "" && in.Source.BodyHash != "" && in.Source.BodyHash != hash {
				continue
			}
		}
		// Only an explicit complete original is a body constraint. Display
		// previews, prepared ASR/OCR and runtime wrappers never become one.
		if in.OriginalText != nil && sourceHash(*in.OriginalText) != hash {
			continue
		}
		out := cloneSource(t.Source)
		out.EventID = t.EventID
		return out, true
	}
	return nil, false
}

func sameSourceMessage(a, b *SourceRef) bool {
	return a != nil && b != nil && a.InstanceID == b.InstanceID && a.MessageID != "" && a.MessageID == b.MessageID
}
