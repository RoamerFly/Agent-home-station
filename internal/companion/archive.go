package companion

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"time"
	"wechat-bot/internal/historyarchive"
)

func (s *Store) archive() historyarchive.Store {
	return historyarchive.Store{Root: filepath.Join(filepath.Dir(s.path), "archive")}
}

// Only retention pruning archives messages. Explicit user clear is handled
// separately and must not silently turn deleted live memory into a new archive.
func (s *Store) pruneStored(scope Scope, c Config, now time.Time) (Scope, error) {
	clean := prune(copyScope(scope), c, now)
	if len(clean.Messages) == len(scope.Messages) {
		return clean, nil
	}
	cutoff := now.Add(-time.Duration(c.MemoryDays) * 24 * time.Hour)
	eligible := 0
	for _, turn := range scope.Messages {
		if !turn.Time.Before(cutoff) {
			eligible++
		}
	}
	extra := max(0, eligible-c.MemoryMessages)
	removed := []Turn{}
	for _, turn := range scope.Messages {
		if turn.Time.Before(cutoff) {
			removed = append(removed, turn)
		} else if extra > 0 {
			removed = append(removed, turn)
			extra--
		}
	}
	for start := 0; start < len(removed); start += 128 {
		batch := Scope{ID: scope.ID, Channel: scope.Channel, Account: scope.Account, Thread: scope.Thread, Messages: removed[start:min(start+128, len(removed))]}
		if _, err := s.archive().Put("memory", scope.ID, batch); err != nil {
			return Scope{}, errors.New("memory archive write failed; active memory retained")
		}
	}
	return clean, nil
}

func (s *Store) ArchiveList(id, after string) ([]historyarchive.Info, string, error) {
	if !validScopeID(id) {
		return nil, "", errors.New("invalid memory scope")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Scopes[id]; !ok {
		return nil, "", errors.New("memory scope missing")
	}
	return s.archive().List("memory", id, after, 20)
}
func (s *Store) ArchiveRead(id, batchID string) (Scope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.data.Scopes[id]
	if !ok {
		return Scope{}, errors.New("memory scope missing")
	}
	batch, _, err := s.archive().Read("memory", id, batchID)
	if err != nil {
		return Scope{}, err
	}
	var scope Scope
	if json.Unmarshal(batch.Data, &scope) != nil || scope.ID != id || scope.Channel != current.Channel || scope.Account != current.Account || scope.Thread != current.Thread {
		return Scope{}, errors.New("memory archive scope mismatch")
	}
	return scope, nil
}
