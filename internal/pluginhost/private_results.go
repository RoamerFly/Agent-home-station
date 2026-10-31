package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
)

// PrivateJobResult is a host-only recovery interface. No HTTP/tool route exposes
// it. check must validate current source versions, forgetting and request scope;
// the store separately validates the installed package and configuration. Callers
// must recheck authority before consuming recovered media or sending anything.
func (s *Store) PrivateJobResult(ctx context.Context, expected Job, check func(context.Context, Job) error) (json.RawMessage, error) {
	denied := errors.New("private plugin result unavailable")
	if check == nil || expected.Context.Account != s.account || expected.State != "completed" || expected.CancelRequested {
		return nil, denied
	}
	if err := check(ctx, expected); err != nil {
		return nil, denied
	}
	var record, payload, manifest, grants []byte
	var revision, enabled, removed, configRevision int
	var digest string
	// One query obtains a coherent snapshot without exposing raw rows to callers.
	err := s.db.QueryRowContext(ctx, `SELECT j.record,p.payload,p.revision,i.digest,i.enabled,i.removed,i.manifest,i.permissions,COALESCE(c.revision,0)
		FROM jobs j JOIN job_private_results p ON p.job_id=j.id
		JOIN installed i ON i.id=j.plugin LEFT JOIN configurations c ON c.plugin=j.plugin WHERE j.id=?`, expected.ID).
		Scan(&record, &payload, &revision, &digest, &enabled, &removed, &manifest, &grants, &configRevision)
	if err != nil {
		return nil, denied
	}
	var current Job
	var installed Package
	var permissions []string
	if json.Unmarshal(record, &current) != nil || json.Unmarshal(manifest, &installed) != nil || json.Unmarshal(grants, &permissions) != nil || current.Revision != revision || enabled == 0 || removed != 0 || current.PackageDigest != digest || current.Plugin != installed.ID || current.Version != installed.Version || current.ConfigRevision != strconv.Itoa(configRevision) || !json.Valid(payload) {
		return nil, denied
	}
	// Compare the durable representation: omitempty normalizes empty/nil arrays
	// after restart without weakening any persisted identity or version field.
	want, err := json.Marshal(expected)
	actual, actualErr := json.Marshal(current)
	if err != nil || actualErr != nil || !bytes.Equal(want, actual) {
		return nil, denied
	}
	if Authorize(installed, Grant{Account: s.account, Plugin: current.Plugin, Version: current.Version, Permissions: permissions}, current.Context, installed.Permissions) != nil {
		return nil, denied
	}
	if err = check(ctx, current); err != nil {
		return nil, denied
	}
	return append(json.RawMessage(nil), payload...), nil
}
