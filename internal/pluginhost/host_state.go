package pluginhost

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
)

const maxHostStateRecordBytes = 1 << 20
const maxHostStateTotalBytes = 8 << 20

var hostStateIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

var ErrHostStateSize = errors.New("host state size exceeds limit")
var ErrHostStateInput = errors.New("invalid host state identifier")
var ErrHostStateCorrupt = errors.New("host state storage corrupt")

// HostState is private host metadata, scoped by this Store's account plus an
// explicit plugin/namespace pair. It is never exposed as plugin configuration,
// a runtime mount, HTTP route, or MCP service.
type HostState struct {
	Revision   int64                      `json:"revision"`
	SourceHash string                     `json:"source_hash"`
	Records    map[string]json.RawMessage `json:"records"`
}

func validHostStateName(value string) bool { return hostStateIdentifier.MatchString(value) }

func validHostStateInput(plugin, namespace string) error {
	if !identifier.MatchString(plugin) || !validHostStateName(namespace) {
		return ErrHostStateInput
	}
	return nil
}

func validHostStateHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateHostStateRecords(records map[string]json.RawMessage) (int, error) {
	total := 0
	for key, payload := range records {
		if !validHostStateName(key) || len(payload) == 0 || !json.Valid(payload) {
			return 0, ErrHostStateInput
		}
		if len(payload) > maxHostStateRecordBytes {
			return 0, ErrHostStateSize
		}
		total += len(payload)
		if total > maxHostStateTotalBytes {
			return 0, ErrHostStateSize
		}
	}
	return total, nil
}

// ReadHostState returns revision zero and an empty record map when this
// host-private namespace has not been initialized.
func (s *Store) ReadHostState(ctx context.Context, plugin, namespace string) (HostState, error) {
	if err := validHostStateInput(plugin, namespace); err != nil {
		return HostState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return HostState{}, err
	}
	defer tx.Rollback()
	state := HostState{Records: map[string]json.RawMessage{}}
	err = tx.QueryRowContext(ctx, `SELECT revision,source_hash FROM host_states WHERE plugin=? AND namespace=?`, plugin, namespace).Scan(&state.Revision, &state.SourceHash)
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.Commit(); err != nil {
			return HostState{}, err
		}
		return state, nil
	}
	if err != nil {
		return HostState{}, err
	}
	if state.Revision < 1 || !validHostStateHash(state.SourceHash) {
		return HostState{}, ErrHostStateCorrupt
	}
	var total int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(payload)),0) FROM host_state_records WHERE plugin=? AND namespace=?`, plugin, namespace).Scan(&total); err != nil {
		return HostState{}, err
	}
	if total < 0 || total > maxHostStateTotalBytes {
		return HostState{}, ErrHostStateCorrupt
	}
	lengths, err := tx.QueryContext(ctx, `SELECT key,length(payload) FROM host_state_records WHERE plugin=? AND namespace=?`, plugin, namespace)
	if err != nil {
		return HostState{}, err
	}
	for lengths.Next() {
		var key string
		var length int
		if err = lengths.Scan(&key, &length); err != nil {
			lengths.Close()
			return HostState{}, err
		}
		if !validHostStateName(key) || length < 1 || length > maxHostStateRecordBytes {
			lengths.Close()
			return HostState{}, ErrHostStateCorrupt
		}
	}
	if err = lengths.Close(); err != nil {
		return HostState{}, err
	}
	if err = lengths.Err(); err != nil {
		return HostState{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT key,payload FROM host_state_records WHERE plugin=? AND namespace=?`, plugin, namespace)
	if err != nil {
		return HostState{}, err
	}
	for rows.Next() {
		var key string
		var payload []byte
		if err = rows.Scan(&key, &payload); err != nil {
			return HostState{}, err
		}
		if !validHostStateName(key) || len(payload) == 0 || len(payload) > maxHostStateRecordBytes || !json.Valid(payload) {
			return HostState{}, ErrHostStateCorrupt
		}
		state.Records[key] = append(json.RawMessage(nil), payload...)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return HostState{}, err
	}
	if err = rows.Close(); err != nil {
		return HostState{}, err
	}
	if err = tx.Commit(); err != nil {
		return HostState{}, err
	}
	return state, nil
}

// InitializeHostState creates a namespace exactly once. It deliberately does
// not require the named package to be installed or enabled, preserving host
// history through a package uninstall.
func (s *Store) InitializeHostState(ctx context.Context, plugin, namespace, sourceHash string, records map[string]json.RawMessage) (HostState, error) {
	if err := validHostStateInput(plugin, namespace); err != nil {
		return HostState{}, err
	}
	if !validHostStateHash(sourceHash) {
		return HostState{}, ErrHostStateInput
	}
	if _, err := validateHostStateRecords(records); err != nil {
		return HostState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HostState{}, err
	}
	defer tx.Rollback()
	var existing int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM host_states WHERE plugin=? AND namespace=?`, plugin, namespace).Scan(&existing)
	if err == nil {
		return HostState{}, ErrRevision
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return HostState{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO host_states(plugin,namespace,revision,source_hash) VALUES(?,?,1,?)`, plugin, namespace, sourceHash); err != nil {
		return HostState{}, err
	}
	for key, payload := range records {
		if _, err = tx.ExecContext(ctx, `INSERT INTO host_state_records(plugin,namespace,key,payload) VALUES(?,?,?,?)`, plugin, namespace, key, []byte(payload)); err != nil {
			return HostState{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return HostState{}, err
	}
	copyRecords := make(map[string]json.RawMessage, len(records))
	for key, payload := range records {
		copyRecords[key] = append(json.RawMessage(nil), payload...)
	}
	return HostState{Revision: 1, SourceHash: sourceHash, Records: copyRecords}, nil
}

// UpdateHostState changes only the named records under a namespace-level CAS.
// It never serializes the namespace's full record set; size accounting queries
// payload lengths and the touched keys only.
func (s *Store) UpdateHostState(ctx context.Context, plugin, namespace string, revision int64, puts map[string]json.RawMessage, deletes []string) (int64, error) {
	if err := validHostStateInput(plugin, namespace); err != nil {
		return 0, err
	}
	if revision < 1 {
		return 0, ErrRevision
	}
	if _, err := validateHostStateRecords(puts); err != nil {
		return 0, err
	}
	seen := make(map[string]bool, len(deletes))
	for _, key := range deletes {
		if !validHostStateName(key) || seen[key] {
			return 0, ErrHostStateInput
		}
		if _, exists := puts[key]; exists {
			return 0, ErrHostStateInput
		}
		seen[key] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var current int64
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM host_states WHERE plugin=? AND namespace=?`, plugin, namespace).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrRevision
		}
		return 0, err
	}
	if current != revision {
		return 0, ErrRevision
	}
	var total int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(payload)),0) FROM host_state_records WHERE plugin=? AND namespace=?`, plugin, namespace).Scan(&total); err != nil {
		return 0, err
	}
	for key, payload := range puts {
		var previous int
		err = tx.QueryRowContext(ctx, `SELECT length(payload) FROM host_state_records WHERE plugin=? AND namespace=? AND key=?`, plugin, namespace, key).Scan(&previous)
		if errors.Is(err, sql.ErrNoRows) {
			previous = 0
		} else if err != nil {
			return 0, err
		}
		total += len(payload) - previous
	}
	for _, key := range deletes {
		var previous int
		err = tx.QueryRowContext(ctx, `SELECT length(payload) FROM host_state_records WHERE plugin=? AND namespace=? AND key=?`, plugin, namespace, key).Scan(&previous)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, err
		}
		total -= previous
	}
	if total > maxHostStateTotalBytes {
		return 0, ErrHostStateSize
	}
	for key, payload := range puts {
		if _, err = tx.ExecContext(ctx, `INSERT INTO host_state_records(plugin,namespace,key,payload) VALUES(?,?,?,?) ON CONFLICT(plugin,namespace,key) DO UPDATE SET payload=excluded.payload`, plugin, namespace, key, []byte(payload)); err != nil {
			return 0, err
		}
	}
	for _, key := range deletes {
		if _, err = tx.ExecContext(ctx, `DELETE FROM host_state_records WHERE plugin=? AND namespace=? AND key=?`, plugin, namespace, key); err != nil {
			return 0, err
		}
	}
	next := current + 1
	result, err := tx.ExecContext(ctx, `UPDATE host_states SET revision=? WHERE plugin=? AND namespace=? AND revision=?`, next, plugin, namespace, current)
	if err != nil {
		return 0, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return 0, ErrRevision
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}
