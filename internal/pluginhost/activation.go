package pluginhost

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// RuntimeConfiguration is private host-to-plugin data. Unlike Configuration,
// Values includes only this plugin's declared secrets; never return it via HTTP.
type RuntimeConfiguration struct {
	Revision int                        `json:"revision"`
	Values   map[string]json.RawMessage `json:"values"`
}

func (s *Store) Account() string { return s.account }

func (s *Store) runtimeConfiguration(ctx context.Context, id string) (RuntimeConfiguration, error) {
	schema, err := s.ConfigSchema(ctx, id)
	if err != nil {
		return RuntimeConfiguration{}, err
	}
	c := RuntimeConfiguration{Values: map[string]json.RawMessage{}}
	var raw, secrets []byte
	err = s.db.QueryRowContext(ctx, `SELECT revision,values_json,secrets_json FROM configurations WHERE plugin=?`, id).Scan(&c.Revision, &raw, &secrets)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	private := map[string]string{}
	if len(raw) > 0 && (json.Unmarshal(raw, &c.Values) != nil || json.Unmarshal(secrets, &private) != nil) {
		return c, errors.New("invalid stored plugin configuration")
	}
	if c.Values == nil || private == nil {
		return RuntimeConfiguration{}, errors.New("invalid stored plugin configuration")
	}
	for name := range c.Values {
		if field, ok := schema.Properties[name]; !ok || field.Secret {
			return RuntimeConfiguration{}, errors.New("private configuration stored in public values")
		}
	}
	for name := range private {
		if field, ok := schema.Properties[name]; !ok || !field.Secret {
			return RuntimeConfiguration{}, errors.New("undeclared secret configuration")
		}
	}
	for name, field := range schema.Properties {
		if field.Secret {
			if v := private[name]; v != "" {
				c.Values[name], _ = json.Marshal(v)
			}
		} else if _, ok := c.Values[name]; !ok && len(field.Default) > 0 {
			c.Values[name] = field.Default
		}
	}
	for name, v := range c.Values {
		field, ok := schema.Properties[name]
		if !ok || validateField(field, v) != nil {
			return RuntimeConfiguration{}, errors.New("plugin configuration requires review")
		}
	}
	for _, name := range schema.Required {
		if _, ok := c.Values[name]; !ok {
			return RuntimeConfiguration{}, errors.New("required plugin configuration missing")
		}
	}
	return c, nil
}

func (s *Store) ConfigurationForExecution(ctx context.Context, id string) (RuntimeConfiguration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.Installed(ctx, id)
	if err != nil {
		return RuntimeConfiguration{}, err
	}
	if !i.Enabled || i.Removed {
		return RuntimeConfiguration{}, errors.New("plugin not enabled")
	}
	return s.runtimeConfiguration(ctx, id)
}

// CheckConfiguration validates a startup plan without enabling a plugin or
// exposing any of its secrets. Actual execution rechecks the current version.
func (s *Store) CheckConfiguration(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.Installed(ctx, id)
	if err != nil {
		return err
	}
	if i.Removed {
		return errors.New("plugin removed")
	}
	_, err = s.runtimeConfiguration(ctx, id)
	return err
}

// ConfigurationForPlugin excludes secrets held exclusively by host proxies.
// ConfigurationForExecution remains private to trusted host code.
func (s *Store) ConfigurationForPlugin(ctx context.Context, id string) (RuntimeConfiguration, error) {
	c, err := s.ConfigurationForExecution(ctx, id)
	if err != nil {
		return c, err
	}
	schema, err := s.ConfigSchema(ctx, id)
	if err != nil {
		return RuntimeConfiguration{}, err
	}
	for name, field := range schema.Properties {
		if field.HostOnly {
			delete(c.Values, name)
		}
	}
	return c, nil
}

// SetEnabled is an administrative reservation, not proof of a running process.
// The controller must confirm termination before clearing it. While reserved,
// configuration edits and package replacement are refused by the same Store.
func (s *Store) SetEnabled(ctx context.Context, id string, revision int, enabled bool) (InstalledPackage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.Installed(ctx, id)
	if err != nil {
		return i, err
	}
	if i.Removed || revision != i.Revision {
		return i, ErrRevision
	}
	if enabled {
		inv := Invocation{Account: s.account, Thread: "host", Requester: "administrator", Purpose: "enable installed plugin", RequestKey: "host-enable"}
		if err = Authorize(i.Package, Grant{s.account, i.Package.ID, i.Package.Version, i.Permissions}, inv, i.Package.Permissions); err != nil {
			return i, err
		}
		if _, err = s.runtimeConfiguration(ctx, id); err != nil {
			return i, err
		}
	}
	if i.Enabled == enabled {
		return i, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return i, err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE installed SET enabled=?,revision=revision+1 WHERE id=? AND revision=? AND removed=0`, enabled, id, revision)
	if err != nil {
		return i, err
	}
	n, err := r.RowsAffected()
	if err != nil || n != 1 {
		return i, ErrRevision
	}
	action := "disabled"
	if enabled {
		action = "enabled"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit(at,plugin,action,digest) VALUES(?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), id, action, i.Digest); err != nil {
		return i, err
	}
	if err = tx.Commit(); err != nil {
		return i, err
	}
	i.Enabled = enabled
	i.Revision++
	return i, nil
}

func (s *Store) PrivateDirectory(ctx context.Context, id string) (string, error) {
	if !identifier.MatchString(id) {
		return "", errors.New("invalid plugin id")
	}
	i, err := s.Installed(ctx, id)
	if err != nil {
		return "", err
	}
	if i.Removed {
		return "", errors.New("plugin removed")
	}
	dir := filepath.Join(s.dir, "data", id)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}
