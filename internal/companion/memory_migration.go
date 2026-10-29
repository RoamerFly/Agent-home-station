package companion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// verifyProjection checks the whole legacy compatibility snapshot (including
// summaries, counts and schedules) plus each unique original's body hash.
func (s *Store) verifyProjection(ctx context.Context) error {
	for id, scope := range s.data.Scopes {
		var raw []byte
		if e := s.db.QueryRowContext(ctx, "SELECT payload FROM scopes WHERE id=?", id).Scan(&raw); e != nil {
			return e
		}
		var stored Scope
		if e := json.Unmarshal(raw, &stored); e != nil {
			return e
		}
		// EventID is new import metadata; all legacy fields must roundtrip exactly.
		for i := range stored.Messages {
			stored.Messages[i].EventID = ""
		}
		comparison := scope
		comparison.Messages = append([]Turn(nil), scope.Messages...)
		for i := range comparison.Messages {
			comparison.Messages[i].EventID = ""
		}
		a, _ := json.Marshal(stored)
		b, _ := json.Marshal(comparison)
		if string(a) != string(b) {
			return errors.New("migration scope snapshot mismatch")
		}
		unique := map[string]Turn{}
		for i, t := range scope.Messages {
			unique[eventID(id, t, i)] = t
		}
		for eid, t := range unique {
			var hash string
			if e := s.db.QueryRowContext(ctx, "SELECT hash FROM events WHERE id=? AND scope=?", eid, id).Scan(&hash); e != nil {
				return e
			}
			body := t.Text
			if t.HasOriginal {
				body = t.FullText
			}
			h := sha256.Sum256([]byte(body))
			if hash != hex.EncodeToString(h[:]) {
				return errors.New("migration source body hash mismatch")
			}
		}
	}
	return nil
}

// DryRunMemory imports a private copy into a disposable sibling directory.
// It never publishes memory.sqlite or changes the live JSON/archive.
func DryRunMemory(ctx context.Context, dir string) (map[string]any, error) {
	before, e := InspectMemory(ctx, dir)
	if e != nil {
		return nil, e
	}
	if before["migration_required"] == false {
		return before, nil
	}
	stage, e := os.MkdirTemp(filepath.Dir(filepath.Clean(dir)), ".memory-dryrun-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(stage)
	b, e := os.ReadFile(filepath.Join(dir, "memory.json"))
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if e == nil {
		if e = os.WriteFile(filepath.Join(stage, "memory.json"), b, 0600); e != nil {
			return nil, e
		}
	}
	s, e := NewStore(stage)
	if e != nil {
		return nil, e
	}
	if e = s.Verify(ctx); e == nil {
		e = s.verifyProjection(ctx)
	}
	closeErr := s.Close()
	if e != nil {
		return nil, e
	}
	if closeErr != nil {
		return nil, closeErr
	}
	result, e := InspectMemory(ctx, stage)
	if e != nil {
		return nil, e
	}
	result["dry_run"] = true
	result["legacy_preserved"] = true
	result["projection_verified"] = true
	return result, nil
}
