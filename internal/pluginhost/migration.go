package pluginhost

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"
)

// MigrationApply is host-only input.  Callers obtain Values and Secrets from
// their own legacy store; neither is accepted from an unauthenticated route or
// returned in MigrationRecord.  A migration is deliberately installed disabled
// and is never enabled as a side effect of a successful configuration copy.
type MigrationApply struct {
	ID              string
	SourceHash      string
	PreparedToken   string
	PreparedDigest  string
	Permissions     []string
	Configuration   ConfigUpdate
	ExpectedPackage string
}

type MigrationRecord struct {
	ID, Plugin, Digest, SourceHash, State, Created, Updated string
	PermissionsHash, ConfigHash                             string
	ConfigRevision, InstalledRevision                       int
}

func (s *Store) Migration(ctx context.Context, id string) (MigrationRecord, error) {
	var r MigrationRecord
	err := s.db.QueryRowContext(ctx, `SELECT id,plugin,digest,source_hash,config_revision,permissions_hash,config_hash,installed_revision,state,created,updated FROM migrations WHERE id=?`, id).Scan(&r.ID, &r.Plugin, &r.Digest, &r.SourceHash, &r.ConfigRevision, &r.PermissionsHash, &r.ConfigHash, &r.InstalledRevision, &r.State, &r.Created, &r.Updated)
	return r, err
}

func migrationHash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func migrationInputHashes(in MigrationApply) (string, string) {
	p := append([]string(nil), in.Permissions...)
	sort.Strings(p)
	secrets := map[string]string{}
	for k, v := range in.Configuration.Secrets {
		if v != "" {
			secrets[k] = migrationHash(v)
		}
	}
	return migrationHash(p), migrationHash(struct {
		Values  map[string]json.RawMessage
		Secrets map[string]string
		Clear   []string
	}{in.Configuration.Values, secrets, in.Configuration.ClearSecrets})
}
func migrationConfigurationMatches(c Configuration, in ConfigUpdate) bool {
	if !reflect.DeepEqual(c.Values, in.Values) {
		return false
	}
	want := []string{}
	for k, v := range in.Secrets {
		if v != "" {
			want = append(want, k)
		}
	}
	sort.Strings(want)
	return reflect.DeepEqual(c.ConfiguredSecrets, want)
}
func (s *Store) migrationPrivateConfigurationMatches(ctx context.Context, id string, c Configuration, in ConfigUpdate) bool {
	if !migrationConfigurationMatches(c, in) {
		return false
	}
	var raw []byte
	if s.db.QueryRowContext(ctx, `SELECT secrets_json FROM configurations WHERE plugin=?`, id).Scan(&raw) != nil {
		return false
	}
	var actual map[string]string
	if json.Unmarshal(raw, &actual) != nil {
		return false
	}
	want := map[string]string{}
	for k, v := range in.Secrets {
		if v != "" {
			want[k] = v
		}
	}
	return migrationHash(actual) == migrationHash(want)
}

// ApplyMigration is a resumable disabled-install saga.  The only interruption
// boundary is after the package is installed disabled and before configuration
// is committed; retrying the identical source/package plan resumes there.  A
// changed source hash/digest/config revision is rejected rather than mixed.
func (s *Store) ApplyMigration(ctx context.Context, in MigrationApply) (InstalledPackage, Configuration, MigrationRecord, error) {
	var zero InstalledPackage
	if in.ID == "" || in.SourceHash == "" || in.PreparedToken == "" || in.PreparedDigest == "" || in.ExpectedPackage == "" {
		return zero, Configuration{}, MigrationRecord{}, errors.New("migration plan incomplete")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	permissionsHash, configHash := migrationInputHashes(in)
	r, err := s.Migration(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		prepared, prepErr := s.Prepared(ctx, in.PreparedToken)
		if prepErr != nil || prepared.Digest != in.PreparedDigest || prepared.Package.ID != in.ExpectedPackage {
			return zero, Configuration{}, MigrationRecord{}, errors.New("migration prepared package changed")
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO migrations(id,plugin,digest,source_hash,config_revision,permissions_hash,config_hash,installed_revision,state,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, in.ID, in.ExpectedPackage, in.PreparedDigest, in.SourceHash, in.Configuration.Revision, permissionsHash, configHash, 0, "pending", now, now)
		if err != nil {
			return zero, Configuration{}, MigrationRecord{}, err
		}
		r = MigrationRecord{ID: in.ID, Plugin: in.ExpectedPackage, Digest: in.PreparedDigest, SourceHash: in.SourceHash, ConfigRevision: in.Configuration.Revision, PermissionsHash: permissionsHash, ConfigHash: configHash, State: "pending", Created: now, Updated: now}
	} else if err != nil {
		return zero, Configuration{}, MigrationRecord{}, err
	}
	if r.Plugin != in.ExpectedPackage || r.Digest != in.PreparedDigest || r.SourceHash != in.SourceHash || r.ConfigRevision != in.Configuration.Revision || r.PermissionsHash != permissionsHash || r.ConfigHash != configHash {
		return zero, Configuration{}, r, errors.New("migration plan changed")
	}
	installed, err := s.Installed(ctx, in.ExpectedPackage)
	if errors.Is(err, sql.ErrNoRows) {
		installed, err = s.Install(ctx, in.PreparedToken, in.PreparedDigest, in.Permissions)
	}
	if err != nil || installed.Enabled || installed.Removed || installed.Digest != in.PreparedDigest {
		if err == nil {
			err = errors.New("migration installation changed")
		}
		return zero, Configuration{}, r, err
	}
	if r.InstalledRevision == 0 {
		_, err = s.db.ExecContext(ctx, `UPDATE migrations SET installed_revision=?,updated=? WHERE id=? AND installed_revision=0`, installed.Revision, time.Now().UTC().Format(time.RFC3339Nano), in.ID)
		if err != nil {
			return zero, Configuration{}, r, err
		}
		r.InstalledRevision = installed.Revision
	}
	if r.InstalledRevision != installed.Revision {
		return zero, Configuration{}, r, errors.New("migration installation changed")
	}
	config, err := s.Configuration(ctx, in.ExpectedPackage)
	if err != nil {
		return zero, Configuration{}, r, err
	}
	if r.State != "complete" {
		if config.Revision != in.Configuration.Revision {
			return zero, Configuration{}, r, errors.New("migration configuration changed")
		}
		config, err = s.saveConfiguration(ctx, in.ExpectedPackage, in.Configuration, func(tx *sql.Tx, revision int) error {
			result, e := tx.ExecContext(ctx, `UPDATE migrations SET state='complete',updated=? WHERE id=? AND state='pending' AND digest=? AND source_hash=? AND permissions_hash=? AND config_hash=? AND installed_revision=?`, time.Now().UTC().Format(time.RFC3339Nano), in.ID, in.PreparedDigest, in.SourceHash, permissionsHash, configHash, r.InstalledRevision)
			if e != nil {
				return e
			}
			n, e := result.RowsAffected()
			if e != nil || n != 1 {
				return errors.New("migration record changed")
			}
			return nil
		})
		if err != nil {
			return zero, Configuration{}, r, err
		}
		r, err = s.Migration(ctx, in.ID)
		if err != nil {
			return zero, Configuration{}, MigrationRecord{}, err
		}
	} else if !s.migrationPrivateConfigurationMatches(ctx, in.ExpectedPackage, config, in.Configuration) {
		return zero, Configuration{}, r, errors.New("migration configuration changed")
	}
	return installed, config, r, nil
}

func (s *Store) Prepared(ctx context.Context, token string) (Prepared, error) {
	var p Prepared
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT digest,manifest,created FROM preparations WHERE token=?`, token).Scan(&p.Digest, &raw, &p.Created)
	if err == nil {
		err = json.Unmarshal(raw, &p.Package)
	}
	p.Token = token
	return p, err
}
