package companion

import (
	"context"
	"encoding/json"
	"time"
)

// LegacyHistoryAllowed checks the whole-scope clear barrier before an import
// lookup, even when the old message was never observed in this store.
func (s *Store) LegacyHistoryAllowed(account, thread string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return account != "" && thread != "" && s.db != nil &&
		s.data.Scopes[ScopeID("wechat", account, thread)].RecallGeneration == 0
}

// NativeRecallAllowed prevents an exact native re-read from bypassing local
// forgetting or source quarantine. Unseen native history may be read, but this
// method never observes/imports it or changes the memory lifecycle.
func (s *Store) NativeRecallAllowed(ctx context.Context, in Input) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := time.Parse(time.RFC3339Nano, in.MessageTime)
	if err != nil || in.Channel == "" || in.Account == "" || in.Thread == "" || in.TraceID == "" || in.Sender == "" || s.db == nil {
		return false, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.sender,e.at,e.text,e.payload,e.suppressed,
	 (SELECT COUNT(*) FROM memory_blocks b WHERE b.scope=e.scope AND b.event_id=e.id)+(SELECT COUNT(*) FROM source_conflicts c WHERE c.scope=e.scope AND c.event_id=e.id)
	 FROM events e WHERE e.scope=? AND e.trace=? AND e.role='user'`, ScopeID(in.Channel, in.Account, in.Thread), in.TraceID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var sender, stamp, text string
		var raw []byte
		var suppressed, blocked int
		if err := rows.Scan(&sender, &stamp, &text, &raw, &suppressed, &blocked); err != nil {
			return false, err
		}
		_, err := time.Parse(time.RFC3339Nano, stamp)
		var turn Turn
		if err != nil || json.Unmarshal(raw, &turn) != nil || sender != in.Sender || suppressed != 0 || blocked != 0 {
			return false, nil
		}
		if in.Source != nil && turn.Source != nil {
			stored, requested := turn.Source, in.Source
			sameInstance := requested.InstanceID != "" && requested.InstanceID == stored.InstanceID
			if requested.InstanceID != "" && !sameInstance || stored.MessageID != requested.MessageID || stored.Account != requested.Account || stored.Thread != requested.Thread || stored.Sender != requested.Sender {
				return false, nil
			}
		}
	}
	return true, rows.Err()
}
