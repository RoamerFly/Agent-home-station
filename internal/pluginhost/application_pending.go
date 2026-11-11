package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
)

// PendingApplicationJobs reads only explicit application operations already
// claimed for execution. A normal local request or an unsubmitted draft is
// excluded by SQLite, before pagination or private source loading.
func (s *Store) PendingApplicationJobs(ctx context.Context, requester, originKind string, after int64, limit int) (JobPage, error) {
	page := JobPage{Items: []Job{}, Next: after, Limit: limit}
	if requester == "" || originKind == "" || after < 0 || limit < 1 {
		return page, errors.New("invalid pending application query")
	}
	const predicate = `plugin LIKE 'application/%' AND json_extract(CAST(record AS TEXT),'$.tool')='interaction'
 AND json_extract(CAST(record AS TEXT),'$.context.requester')=?
 AND json_extract(CAST(record AS TEXT),'$.arguments.draft.origin.kind')=?
 AND COALESCE(json_extract(CAST(record AS TEXT),'$.cancel_requested'),0)=0
 AND json_extract(CAST(record AS TEXT),'$.state') IN ('submitting','running')`
	rows, err := s.db.QueryContext(ctx, `SELECT seq,record FROM jobs WHERE `+predicate+` AND seq>? ORDER BY seq LIMIT ?`, requester, originKind, after, limit)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var seq int64
		var raw []byte
		var job Job
		if err := rows.Scan(&seq, &raw); err != nil {
			rows.Close()
			return page, err
		}
		if json.Unmarshal(raw, &job) != nil {
			rows.Close()
			return page, errors.New("invalid pending application job")
		}
		page.Next = seq
		page.Items = append(page.Items, job)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	var more int
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE `+predicate+` AND seq>?)`, requester, originKind, page.Next).Scan(&more)
	page.HasMore = more != 0
	return page, err
}
