package companion

import (
	"context"
	"database/sql"
	"errors"
)

// LocalToolCheckpoint contains no arguments, file contents or process output.
// An empty result means dispatch was reserved but the outcome is unverified.
type LocalToolCheckpoint struct {
	Key, Tool, Grant, Target, Result string
}

type LocalToolRun struct {
	store      *Store
	eventID    string
	revision   int
	Tracked    bool
	Operations []LocalToolCheckpoint
}

const visibleLocalToolEvent = `e.suppressed=0
 AND NOT EXISTS(SELECT 1 FROM source_conflicts sc WHERE sc.event_id=e.id)
 AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=e.scope AND b.event_id=e.id)`

// LocalToolEvidence returns only a chosen tool's journal metadata for an exact
// visible local source. It does not load the original message or tool bodies
// into model context, and cannot be used to select another conversation.
func (s *Store) LocalToolEvidence(ctx context.Context, in Input, tool string) ([]LocalToolCheckpoint, error) {
	scope, err := localConversationScope(in)
	if err != nil || tool == "" || in.Source == nil || in.Source.EventID == "" || in.Source.InstanceID == "" || in.Source.Revision < 1 {
		return nil, ErrLocalConversationIdentity
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, errors.New("memory database closed")
	}
	source := in.Source
	if err := s.localRequestSourceLocked(ctx, scope, source.MessageID, *source); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.call_key,c.tool,c.grant_key,c.target,c.result FROM events e
 LEFT JOIN local_tool_checkpoints c ON c.event_id=e.id AND c.revision=e.revision AND c.tool=? AND c.call_key!=''
 WHERE e.id=? AND e.scope=? AND e.revision=? AND e.role='user' AND `+visibleLocalToolEvent+` ORDER BY c.rowid`, tool, source.EventID, scope, source.Revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []LocalToolCheckpoint
	visible := false
	for rows.Next() {
		visible = true
		var key, name, grant, target, result sql.NullString
		if err := rows.Scan(&key, &name, &grant, &target, &result); err != nil {
			return nil, err
		}
		if key.Valid {
			results = append(results, LocalToolCheckpoint{Key: key.String, Tool: name.String, Grant: grant.String, Target: target.String, Result: result.String})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !visible {
		return nil, ErrLocalRequestUnavailable
	}
	return results, nil
}

// OpenLocalToolRun attaches the journal to the exact visible source revision.
// An empty-key row identifies a turn admitted under the checkpoint protocol.
// An older unfinished turn without that row may have already acted.
func (s *Store) OpenLocalToolRun(ctx context.Context, in Input, fresh bool) (*LocalToolRun, error) {
	scope, err := localConversationScope(in)
	if err != nil || in.TraceID == "" || in.OriginalText == nil {
		return nil, ErrLocalConversationIdentity
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, errors.New("memory database closed")
	}
	run := &LocalToolRun{store: s}
	err = s.db.QueryRowContext(ctx, `SELECT e.id,e.revision FROM events e
 WHERE e.scope=? AND e.trace=? AND e.role='user' AND e.text=? AND `+visibleLocalToolEvent, scope, in.TraceID, *in.OriginalText).Scan(&run.eventID, &run.revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLocalRequestUnavailable
	}
	if err != nil {
		return nil, err
	}
	if fresh {
		_, err = s.db.ExecContext(ctx, `INSERT INTO local_tool_checkpoints(event_id,revision,call_key,tool,grant_key,target)
 VALUES(?,?,'','','','') ON CONFLICT DO NOTHING`, run.eventID, run.revision)
		if err != nil {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT call_key,tool,grant_key,target,result FROM local_tool_checkpoints
 WHERE event_id=? AND revision=? ORDER BY rowid`, run.eventID, run.revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var op LocalToolCheckpoint
		if err := rows.Scan(&op.Key, &op.Tool, &op.Grant, &op.Target, &op.Result); err != nil {
			return nil, err
		}
		if op.Key == "" {
			run.Tracked = true
		} else {
			run.Operations = append(run.Operations, op)
		}
	}
	return run, rows.Err()
}

// Reserve must succeed durably before a mutating tool is dispatched. Existing
// checkpoints and obsolete sources cannot be dispatched through this method.
func (r *LocalToolRun) Reserve(ctx context.Context, op LocalToolCheckpoint) error {
	if !r.Tracked || op.Key == "" || op.Tool == "" {
		return ErrLocalRequestUnavailable
	}
	return r.update(ctx, `INSERT INTO local_tool_checkpoints(event_id,revision,call_key,tool,grant_key,target)
 SELECT e.id,e.revision,?,?,?,? FROM events e WHERE e.id=? AND e.revision=? AND `+visibleLocalToolEvent+
		` ON CONFLICT DO NOTHING`, op.Key, op.Tool, op.Grant, op.Target, r.eventID, r.revision)
}

func (r *LocalToolRun) RecordResult(ctx context.Context, key, result string) error {
	if key == "" || result == "" {
		return ErrLocalRequestUnavailable
	}
	return r.update(ctx, `UPDATE local_tool_checkpoints SET result=? WHERE event_id=? AND revision=?
 AND call_key=? AND result='' AND EXISTS(SELECT 1 FROM events e
 WHERE e.id=local_tool_checkpoints.event_id AND e.revision=local_tool_checkpoints.revision AND `+visibleLocalToolEvent+`)`,
		result, r.eventID, r.revision, key)
}

func (r *LocalToolRun) CheckSource(ctx context.Context) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if r.store.db == nil {
		return errors.New("memory database closed")
	}
	var visible bool
	err := r.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events e
 WHERE e.id=? AND e.revision=? AND `+visibleLocalToolEvent+`)`, r.eventID, r.revision).Scan(&visible)
	if err != nil {
		return err
	}
	if !visible {
		return ErrLocalRequestUnavailable
	}
	return nil
}

func (r *LocalToolRun) update(ctx context.Context, statement string, args ...any) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if r.store.db == nil {
		return errors.New("memory database closed")
	}
	result, err := r.store.db.ExecContext(ctx, statement, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLocalRequestUnavailable
	}
	return nil
}
