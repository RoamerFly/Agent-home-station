package companion

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
)

// InspectMemory never initializes a database or prints conversation content.
func InspectMemory(ctx context.Context, dir string) (map[string]any, error) {
	path := filepath.Join(dir, "memory.sqlite")
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		legacy, e := readLegacyStore(dir)
		if e != nil {
			return nil, e
		}
		messages := 0
		for _, scope := range legacy.data.Scopes {
			messages += len(scope.Messages)
		}
		return map[string]any{"backend": "legacy_json", "scopes": len(legacy.data.Scopes), "active_messages": messages, "migration_required": true, "read_only": true}, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("unsafe memory database")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(abs)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var integrity string
	if err = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return nil, err
	}
	if integrity != "ok" {
		return nil, errors.New("database integrity verification failed")
	}
	counts := map[string]any{"backend": "sqlite_wal", "integrity": "ok", "bytes": st.Size(), "read_only": true, "migration_required": false}
	for _, table := range []string{"scopes", "events", "memories", "embeddings"} {
		var n int64
		if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			return nil, err
		}
		counts[table] = n
	}
	return counts, nil
}
