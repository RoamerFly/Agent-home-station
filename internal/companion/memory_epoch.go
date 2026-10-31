package companion

import "context"

// DataEpoch invalidates retained memory pages after correction, forgetting or
// source changes. It contains no message contents or authorization material.
func (s *Store) DataEpoch(ctx context.Context) (string, error) {
	if s.db == nil {
		return "0", nil
	}
	var epoch string
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE((SELECT value FROM meta WHERE key='data_epoch'),'0')").Scan(&epoch)
	return epoch, err
}
