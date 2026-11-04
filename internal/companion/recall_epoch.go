package companion

import (
	"context"
	"database/sql"
	"errors"
)

const selectRecallEpoch = "SELECT COALESCE((SELECT value FROM meta WHERE key='recall_epoch'),'0')"

// RecallEpoch changes when retained evidence may no longer be used as stated.
// Independent incoming messages and accepted assistant replies do not change it.
// DataEpoch remains the stricter snapshot/pagination consistency marker.
func (s *Store) RecallEpoch(ctx context.Context) (string, error) {
	if s.db == nil {
		return "", errors.New("memory database closed")
	}
	var epoch string
	err := s.db.QueryRowContext(ctx, selectRecallEpoch).Scan(&epoch)
	return epoch, err
}

func bumpRecallEpoch(tx *sql.Tx) error {
	_, err := tx.Exec("INSERT INTO meta(key,value) VALUES('recall_epoch','1') ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1")
	return err
}
