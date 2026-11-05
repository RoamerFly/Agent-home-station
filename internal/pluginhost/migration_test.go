package pluginhost

import (
	"context"
	"encoding/json"
	"testing"
)

func TestApplyMigrationInstallsDisabledAndRetriesWithoutDuplicate(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(t.TempDir(), "migration-account")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.Prepare(ctx, readyFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	in := MigrationApply{ID: "legacy-speech-v1", SourceHash: "source-hash-1", PreparedToken: p.Token, PreparedDigest: p.Digest, ExpectedPackage: p.Package.ID, Permissions: []string{"artifact.read"}, Configuration: ConfigUpdate{Values: map[string]json.RawMessage{"backend": json.RawMessage(`"legacy"`)}}}
	i, c, r, err := s.ApplyMigration(ctx, in)
	if err != nil || i.Enabled || r.State != "complete" || c.Revision != 1 || string(c.Values["backend"]) != `"legacy"` {
		t.Fatalf("apply: %+v %+v %+v %v", i, c, r, err)
	}
	// The preparation was consumed, so this proves a completed retry relies on
	// the durable record instead of a second install or a recreated package.
	i2, c2, r2, err := s.ApplyMigration(ctx, in)
	if err != nil || i2.Revision != i.Revision || c2.Revision != c.Revision || r2.State != "complete" {
		t.Fatalf("retry: %+v %+v %+v %v", i2, c2, r2, err)
	}
	in.SourceHash = "changed"
	if _, _, _, err = s.ApplyMigration(ctx, in); err == nil {
		t.Fatal("changed legacy source accepted")
	}
}

func TestApplyMigrationResumesInterruptedDisabledInstall(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(t.TempDir(), "resume-account")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.Prepare(ctx, readyFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	in := MigrationApply{ID: "legacy-resume-v1", SourceHash: "source-hash-2", PreparedToken: p.Token, PreparedDigest: p.Digest, ExpectedPackage: p.Package.ID, Permissions: []string{"artifact.read"}, Configuration: ConfigUpdate{Values: map[string]json.RawMessage{"backend": json.RawMessage(`"legacy"`)}}}
	// Simulate a process interruption after the durable plan record and disabled
	// package install, but before configuration and completion are committed.
	ph, ch := migrationInputHashes(in)
	if _, err = s.db.ExecContext(ctx, `INSERT INTO migrations(id,plugin,digest,source_hash,config_revision,permissions_hash,config_hash,state,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?)`, in.ID, in.ExpectedPackage, in.PreparedDigest, in.SourceHash, 0, ph, ch, "pending", "t", "t"); err != nil {
		t.Fatal(err)
	}
	installed, err := s.Install(ctx, p.Token, p.Digest, in.Permissions)
	if err != nil || installed.Enabled {
		t.Fatalf("interrupted install: %+v %v", installed, err)
	}
	installed, config, record, err := s.ApplyMigration(ctx, in)
	if err != nil || installed.Enabled || config.Revision != 1 || record.State != "complete" {
		t.Fatalf("resume: %+v %+v %+v %v", installed, config, record, err)
	}
}

func TestApplyMigrationRejectsConcurrentConfigurationEdit(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(t.TempDir(), "race-account")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.Prepare(ctx, readyFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	in := MigrationApply{ID: "legacy-race-v1", SourceHash: "source-race", PreparedToken: p.Token, PreparedDigest: p.Digest, ExpectedPackage: p.Package.ID, Permissions: []string{"artifact.read"}, Configuration: ConfigUpdate{Values: map[string]json.RawMessage{"backend": json.RawMessage(`"legacy"`)}}}
	ph, ch := migrationInputHashes(in)
	if _, err = s.db.ExecContext(ctx, `INSERT INTO migrations(id,plugin,digest,source_hash,config_revision,permissions_hash,config_hash,state,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?)`, in.ID, in.ExpectedPackage, in.PreparedDigest, in.SourceHash, 0, ph, ch, "pending", "t", "t"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Install(ctx, p.Token, p.Digest, in.Permissions); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveConfiguration(ctx, in.ExpectedPackage, ConfigUpdate{Values: map[string]json.RawMessage{"backend": json.RawMessage(`"other"`)}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.ApplyMigration(ctx, in); err == nil {
		t.Fatal("concurrent configuration edit completed migration")
	}
	if r, e := s.Migration(ctx, in.ID); e != nil || r.State != "pending" {
		t.Fatalf("migration state changed: %+v %v", r, e)
	}
}
