package companion

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ReconcileLegacySource is for trusted adapters which have just resolved an
// actual native message. It repairs only a historical automatic collision
// between a current, exact identity and an unknown legacy dataset. It never
// clears forgetting, manual quarantine, or conflicts within the current dataset.
func (s *Store) ReconcileLegacySource(ctx context.Context, in Input) (*SourceRef, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.Source == nil || in.Source.InstanceID != s.SourceInstanceID() || in.Source.ServerID == "" || in.Sender == "" || in.TraceID == "" {
		return nil, false, nil
	}
	at, err := time.Parse(time.RFC3339Nano, in.MessageTime)
	if err != nil {
		return nil, false, nil
	}
	id := ScopeID(in.Channel, in.Account, in.Thread)
	sc := copyScope(s.data.Scopes[id])
	position, legacy := -1, false
	for i, turn := range sc.Messages {
		if turn.Role != "user" || turn.TraceID != in.TraceID {
			continue
		}
		ref := turn.Source
		if ref == nil || ref.InstanceID == "" {
			if turn.Sender != in.Sender || !turn.Time.Equal(at) || ref != nil && ref.ServerID != in.Source.ServerID {
				legacy = true
			}
			continue
		}
		if ref.InstanceID != in.Source.InstanceID {
			continue
		}
		if turn.Sender != in.Sender || !turn.Time.Equal(at) || ref.ServerID != in.Source.ServerID || ref.Account != in.Account || ref.Thread != in.Thread || ref.MessageID != in.TraceID || position >= 0 {
			return nil, false, nil
		}
		position = i
	}
	if position < 0 || !legacy {
		return nil, false, nil
	}
	turn := sc.Messages[position]
	if turn.Source.Integrity != "conflicted" {
		return nil, false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var raw []byte
	var revision, suppressed, blocked int
	var hash, text string
	if err = tx.QueryRowContext(ctx, "SELECT revision,suppressed,hash,text,payload FROM events WHERE id=? AND scope=?", turn.EventID, id).Scan(&revision, &suppressed, &hash, &text, &raw); err != nil {
		return nil, false, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_blocks WHERE scope=? AND event_id=?", id, turn.EventID).Scan(&blocked); err != nil {
		return nil, false, err
	}
	if suppressed != 0 || blocked != 0 || turn.Source.Revision != revision || turn.Source.BodyHash != hash {
		return nil, false, nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT reason FROM source_conflicts WHERE scope=? AND event_id=?", id, turn.EventID)
	if err != nil {
		return nil, false, err
	}
	count, automatic := 0, true
	for rows.Next() {
		var reason string
		if err = rows.Scan(&reason); err != nil {
			rows.Close()
			return nil, false, err
		}
		count++
		automatic = automatic && reason == "source_identity_conflict"
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	if count == 0 || !automatic {
		return nil, false, nil
	}
	var persisted Turn
	if json.Unmarshal(raw, &persisted) != nil || persisted.Source == nil || !sameSourceMessage(persisted.Source, turn.Source) || persisted.Source.BodyHash != hash || persisted.Source.ServerID != in.Source.ServerID || persisted.Sender != in.Sender || !persisted.Time.Equal(at) {
		return nil, false, errors.New("source_reconciliation_snapshot_changed")
	}
	turn.Source = cloneSource(turn.Source)
	turn.Source.Integrity = "verified_import"
	turn.Source.Revision = revision + 1
	for i := range turn.IncomingMedia {
		if turn.IncomingMedia[i].Source.MessageID == "" || turn.IncomingMedia[i].Source.MessageID == in.TraceID {
			turn.IncomingMedia[i].Source = *turn.Source
		}
	}
	encoded, _ := json.Marshal(turn)
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO event_revisions(event_id,revision,text,hash) VALUES(?,?,?,?)", turn.EventID, revision, text, hash); err != nil {
		return nil, false, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO event_revisions(event_id,revision,text,hash) VALUES(?,?,?,?)", turn.EventID, revision+1, text, hash); err != nil {
		return nil, false, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE events SET revision=?,payload=? WHERE id=? AND revision=?", revision+1, encoded, turn.EventID, revision); err != nil {
		return nil, false, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM source_conflicts WHERE scope=? AND event_id=? AND reason='source_identity_conflict'", id, turn.EventID); err != nil {
		return nil, false, err
	}
	// Old derived interpretations do not regain authority by restoring raw
	// provenance. They require fresh extraction against the new revision.
	if _, err = tx.ExecContext(ctx, "UPDATE memories SET status='conflicted',version=version+1 WHERE status='active' AND id IN(SELECT memory_id FROM memory_sources WHERE event_id=?)", turn.EventID); err != nil {
		return nil, false, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM embeddings WHERE memory_id IN(SELECT memory_id FROM memory_sources WHERE event_id=?)", turn.EventID); err != nil {
		return nil, false, err
	}
	sc.Messages[position] = turn
	sc.Summary = ""
	sc.CompressedCount = 0
	sc.SummaryBoundary = 0
	encoded, _ = json.Marshal(sc)
	if _, err = tx.ExecContext(ctx, "UPDATE scopes SET payload=? WHERE id=?", encoded, id); err != nil {
		return nil, false, err
	}
	if err = bumpMemoryEpoch(tx); err != nil {
		return nil, false, err
	}
	if err = bumpRecallEpoch(tx); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	s.data.Scopes[id] = sc
	return cloneSource(turn.Source), true, nil
}
