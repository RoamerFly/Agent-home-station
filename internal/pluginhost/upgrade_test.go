package pluginhost

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func prepareUpgradeFixture(t *testing.T, s *Store, installed InstalledPackage, change func(*Package, *ConfigSchema)) Prepared {
	t.Helper()
	source, _ := s.PackageDirectory(installed.Digest)
	dir := t.TempDir()
	if err := snapshot(t.Context(), source, dir); err != nil {
		t.Fatal(err)
	}
	var p Package
	manifest, _ := json.Marshal(installed.Package)
	if err := json.Unmarshal(manifest, &p); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := parseConfigSchema(raw)
	if err != nil {
		t.Fatal(err)
	}
	p.Version += "-upgrade"
	if change != nil {
		change(&p, &schema)
	}
	raw, _ = json.Marshal(schema)
	if err = os.WriteFile(filepath.Join(dir, "config.schema.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	p.Files["config.schema.json"] = hex.EncodeToString(sum[:])
	raw, _ = json.Marshal(p)
	if err = os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	prepared, err := s.Prepare(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

// Compare original database bytes as well as the redacted public view. An
// upgrade must not rewrite private values, renumber history or synthesize edits.
func configurationBytes(t *testing.T, s *Store, id string) []byte {
	t.Helper()
	rows, err := s.db.Query(`SELECT revision,values_json,secrets_json FROM configuration_versions WHERE plugin=? ORDER BY revision`, id)
	if err != nil {
		t.Fatal(err)
	}
	var history [][]any
	for rows.Next() {
		var revision int
		var values, secrets []byte
		if err = rows.Scan(&revision, &values, &secrets); err != nil {
			t.Fatal(err)
		}
		history = append(history, []any{revision, string(values), string(secrets)})
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	var revision int
	var values, secrets []byte
	if err = s.db.QueryRow(`SELECT revision,values_json,secrets_json FROM configurations WHERE plugin=?`, id).Scan(&revision, &values, &secrets); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]any{revision, string(values), string(secrets), history})
	return raw
}

func TestInstallUpgradePreservesPrivateConfigurationHistoryAndUnknownJobs(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	s, err := OpenStore(dir, "a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	i := installConfigFixture(t, s)
	c, err := s.SaveConfiguration(ctx, i.Package.ID, ConfigUpdate{Secrets: map[string]string{"key": "synthetic-upgrade-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.SaveConfiguration(ctx, i.Package.ID, ConfigUpdate{Revision: c.Revision, Values: map[string]json.RawMessage{"count": json.RawMessage(`7`)}})
	if err != nil {
		t.Fatal(err)
	}
	before := configurationBytes(t, s, i.Package.ID)
	i, err = s.SetEnabled(ctx, i.Package.ID, i.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	request := Job{Plugin: i.Package.ID, Version: i.Package.Version, PackageDigest: i.Digest, ConfigRevision: strconv.Itoa(c.Revision), Tool: "speech.synthesize", Arguments: json.RawMessage(`{"synthetic":true}`), Context: Invocation{Account: "a", Thread: "thread", Requester: "owner", Purpose: "synthetic upgrade contract", RequestKey: "unknown-before-upgrade"}}
	job, _, err := s.CreateJob(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	job, err = s.ClaimJob(ctx, job.ID, job.Revision)
	if err != nil {
		t.Fatal(err)
	}
	prepared := prepareUpgradeFixture(t, s, i, func(_ *Package, schema *ConfigSchema) {
		schema.Properties["new_option"] = ConfigField{Type: "boolean", Default: json.RawMessage(`true`)}
		field := schema.Properties["key"]
		field.HostOnly = true // More restrictive visibility preserves the same secret.
		schema.Properties["key"] = field
	})
	if _, err = s.Install(ctx, prepared.Token, prepared.Digest, prepared.Package.Permissions); err == nil {
		t.Fatal("enabled upgrade bypassed drain reservation")
	}
	i, err = s.SetEnabled(ctx, i.Package.ID, i.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := s.Install(ctx, prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Enabled || upgraded.Digest == i.Digest || upgraded.Revision != i.Revision+1 || !bytes.Equal(before, configurationBytes(t, s, i.Package.ID)) {
		t.Fatal("upgrade changed retained configuration or unsafe activation state")
	}
	if _, err = s.SetEnabled(ctx, upgraded.Package.ID, upgraded.Revision, true); err != nil {
		t.Fatal(err)
	}
	host, err := s.ConfigurationForExecution(ctx, i.Package.ID)
	if err != nil || host.Revision != c.Revision || string(host.Values["key"]) != `"synthetic-upgrade-secret"` || string(host.Values["new_option"]) != "true" {
		t.Fatal("compatible effective configuration lost", err)
	}
	plugin, err := s.ConfigurationForPlugin(ctx, i.Package.ID)
	if err != nil || len(plugin.Values["key"]) != 0 {
		t.Fatal("host-only secret exposed to plugin", err)
	}
	public, _ := s.Configuration(ctx, i.Package.ID)
	raw, _ := json.Marshal(public)
	if bytes.Contains(raw, []byte("synthetic-upgrade-secret")) || public.Revision != c.Revision {
		t.Fatal("public view exposed secret or reset revision")
	}
	request.PackageDigest, request.Version = upgraded.Digest, upgraded.Package.Version
	if _, _, err = s.CreateJob(ctx, request); !errors.Is(err, ErrRequestConflict) {
		t.Fatal("old request was rebound to new package", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir, "a")
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := s.Job(ctx, job.ID)
	if err != nil || !reflect.DeepEqual(persisted, job) || persisted.Recovery() != "needs_review" || !bytes.Equal(before, configurationBytes(t, s, i.Package.ID)) {
		t.Fatal("reopen changed original unknown job or configuration", err)
	}
}

func TestInstallUpgradeIncompatibleSchemaRetainsOriginalAndApproval(t *testing.T) {
	changes := map[string]func(*Package, *ConfigSchema){
		"remove_public": func(_ *Package, s *ConfigSchema) { delete(s.Properties, "count") },
		"change_type": func(_ *Package, s *ConfigSchema) {
			f := s.Properties["count"]
			f.Type = "string"
			s.Properties["count"] = f
		},
		"new_required": func(_ *Package, s *ConfigSchema) {
			s.Properties["required_new"] = ConfigField{Type: "string"}
			s.Required = append(s.Required, "required_new")
		},
		"public_to_secret": func(_ *Package, s *ConfigSchema) { s.Properties["count"] = ConfigField{Type: "string", Secret: true} },
		"remove_secret":    func(_ *Package, s *ConfigSchema) { delete(s.Properties, "key") },
		"secret_to_public": func(_ *Package, s *ConfigSchema) { s.Properties["key"] = ConfigField{Type: "string"} },
		"secret_bounds": func(_ *Package, s *ConfigSchema) {
			f := s.Properties["key"]
			maximum := 2
			f.MaxLength = &maximum
			s.Properties["key"] = f
		},
		"host_secret_visibility": func(_ *Package, s *ConfigSchema) {
			f := s.Properties["key"]
			f.HostOnly = false
			s.Properties["key"] = f
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, err := OpenStore(t.TempDir(), "a")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			i := installConfigFixture(t, s)
			if name == "host_secret_visibility" {
				p := prepareUpgradeFixture(t, s, i, func(_ *Package, schema *ConfigSchema) {
					f := schema.Properties["key"]
					f.HostOnly = true
					schema.Properties["key"] = f
				})
				i, err = s.Install(t.Context(), p.Token, p.Digest, p.Package.Permissions)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.SaveConfiguration(t.Context(), i.Package.ID, ConfigUpdate{Secrets: map[string]string{"key": "synthetic-private-upgrade-key"}}); err != nil {
				t.Fatal(err)
			}
			before := configurationBytes(t, s, i.Package.ID)
			prepared := prepareUpgradeFixture(t, s, i, change)
			if _, err = s.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions); !errors.Is(err, ErrConfigurationMigrationRequired) || strings.Contains(err.Error(), "synthetic-private") {
				t.Fatal("incompatible upgrade did not fail safely", err)
			}
			current, err := s.Installed(t.Context(), i.Package.ID)
			if err != nil || !reflect.DeepEqual(i, current) || !bytes.Equal(before, configurationBytes(t, s, i.Package.ID)) {
				t.Fatal("rejected upgrade changed original installation", err)
			}
			var token string
			if err = s.db.QueryRow(`SELECT token FROM preparations WHERE token=?`, prepared.Token).Scan(&token); err != nil {
				t.Fatal("rejection consumed original approval", err)
			}
		})
	}
}

func TestInstallUpgradeWriteFailureRollsBackAndGrantChangeIsExplicit(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := installConfigFixture(t, s)
	if _, err = s.SaveConfiguration(t.Context(), i.Package.ID, ConfigUpdate{Secrets: map[string]string{"key": "synthetic-disk-key"}}); err != nil {
		t.Fatal(err)
	}
	before := configurationBytes(t, s, i.Package.ID)
	prepared := prepareUpgradeFixture(t, s, i, nil)
	if _, err = s.db.Exec(`CREATE TRIGGER upgrade_synthetic_disk_failure BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT,'synthetic disk failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Install(t.Context(), prepared.Token, prepared.Digest, []string{"model.invoke"}); err == nil {
		t.Fatal("write failure accepted")
	}
	current, _ := s.Installed(t.Context(), i.Package.ID)
	if !reflect.DeepEqual(i, current) || !bytes.Equal(before, configurationBytes(t, s, i.Package.ID)) {
		t.Fatal("failed commit changed installation or secrets")
	}
	if _, err = s.db.Exec(`DROP TRIGGER upgrade_synthetic_disk_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Install(t.Context(), prepared.Token, prepared.Digest, []string{"undeclared"}); err == nil {
		t.Fatal("new undeclared permission accepted")
	}
	upgraded, err := s.Install(t.Context(), prepared.Token, prepared.Digest, []string{"model.invoke"})
	if err != nil || !reflect.DeepEqual(upgraded.Permissions, []string{"model.invoke"}) || upgraded.Enabled || !bytes.Equal(before, configurationBytes(t, s, i.Package.ID)) {
		t.Fatal("explicit reduced grant reset config or inherited permissions", err)
	}
	if _, err = s.SetEnabled(t.Context(), i.Package.ID, upgraded.Revision, true); err == nil {
		t.Fatal("reduced grant bypassed whole-package permission confirmation")
	}
	prepared = prepareUpgradeFixture(t, s, upgraded, func(p *Package, _ *ConfigSchema) { p.Permissions = append(p.Permissions, "device") })
	upgraded, err = s.Install(t.Context(), prepared.Token, prepared.Digest, []string{"model.invoke"})
	if err != nil || containsString(upgraded.Permissions, "device") {
		t.Fatal("new package permission silently granted", err)
	}
	if _, err = s.SetEnabled(t.Context(), i.Package.ID, upgraded.Revision, true); err == nil {
		t.Fatal("unconfirmed expanded permission activated")
	}
}
