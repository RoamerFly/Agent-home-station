package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// GlobalMessages preserves legacy substring/latest ordering while SQL performs
// filtering and bounded paging. Only the requested prefix is materialized; raw
// conversation payloads are never all decoded in the caller.
func (s *Store) GlobalMessages(ctx context.Context, labels map[string]string, query, since, before string, limit int) ([]MemoryEvent, int, error) {
	items := []MemoryEvent{}
	if len(labels) == 0 {
		return items, 0, nil
	}
	ids := []string{}
	for id := range labels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := []string{}
	args := []any{}
	for _, id := range ids {
		parts = append(parts, "(?,?)")
		args = append(args, id, labels[id])
	}
	cte := "WITH labels(scope,label) AS (VALUES " + strings.Join(parts, ",") + ") "
	where := " FROM events e JOIN labels l ON l.scope=e.scope WHERE e.active=1 AND e.suppressed=0 AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=e.scope AND b.event_id=e.id)"
	if query != "" {
		where += " AND instr(lower(e.text||l.label),?)>0"
		args = append(args, strings.ToLower(query))
	}
	if since != "" {
		where += " AND e.at>=?"
		stamp, err := time.Parse(time.RFC3339Nano, since)
		if err != nil {
			return items, 0, err
		}
		args = append(args, memoryTime(stamp))
	}
	if before != "" {
		where += " AND e.at<?"
		stamp, err := time.Parse(time.RFC3339Nano, before)
		if err != nil {
			return items, 0, err
		}
		args = append(args, memoryTime(stamp))
	}
	var total int
	if err := s.db.QueryRowContext(ctx, cte+"SELECT COUNT(*)"+where, args...).Scan(&total); err != nil {
		return items, 0, err
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, cte+"SELECT e.id,e.scope,e.trace,e.role,e.sender,e.at,e.text,e.hash,e.complete,e.revision,e.seq,e.payload"+where+" ORDER BY e.at DESC,e.id LIMIT ?", args...)
	if err != nil {
		return items, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var e MemoryEvent
		var payload []byte
		if err = rows.Scan(&e.ID, &e.Scope, &e.Trace, &e.Role, &e.Sender, &e.At, &e.Text, &e.Hash, &e.Complete, &e.Revision, &e.Seq, &payload); err != nil {
			return items, 0, err
		}
		var t Turn
		_ = json.Unmarshal(payload, &t)
		e.SenderName = t.SenderName
		e.Media = t.Media
		items = append(items, e)
	}
	return items, total, rows.Err()
}

func (s *Store) ScopeIdentities() []Scope {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Scope{}
	for _, v := range s.data.Scopes {
		v.Messages = nil
		v.Notes = nil
		out = append(out, v)
	}
	return out
}

func (s *Store) ArchiveProgress(ctx context.Context, id string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", "archive:"+id).Scan(&v)
	if err != nil {
		return "not_started", nil
	}
	if v == "done" {
		return "complete", nil
	}
	return fmt.Sprintf("partial:%s", v), nil
}
