package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func privateResultFixture(t *testing.T) (*Store, Job, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(dir, "a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	i := installConfigFixture(t, s)
	if _, err = s.db.Exec(`UPDATE installed SET enabled=1 WHERE id='speech'`); err != nil {
		t.Fatal(err)
	}
	j, _, err := s.CreateJob(t.Context(), Job{Plugin: "speech", Version: i.Package.Version, PackageDigest: i.Digest, ConfigRevision: "0", Tool: "synthesize", Context: Invocation{Account: "a", Thread: "t", Requester: "u", Purpose: "synthetic", RequestKey: "private-handoff", SourceVersions: map[string]int{"source": 1}}, Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	j, err = s.ClaimJob(t.Context(), j.ID, j.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return s, j, dir
}

var syntheticPrivate = json.RawMessage(`{"locator":"https://media.example/video?token=private-fixture","key":"18446744073709551615"}`)

func allowPrivateFixture(context.Context, Job) error { return nil }

func TestPrivateResultAtomicRestartAndPublicIsolation(t *testing.T) {
	s, j, dir := privateResultFixture(t)
	finished, err := s.CompleteJobWithPrivate(t.Context(), j.ID, j.Revision, nil, json.RawMessage(`{"coverage":"media_locator_only"}`), syntheticPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := s.PrivateJobResult(t.Context(), finished, allowPrivateFixture); err != nil || !bytes.Equal(raw, syntheticPrivate) {
		t.Fatal("fresh in-memory checkpoint cannot recover handoff", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir, "a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	persisted, err := s.Job(t.Context(), finished.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.PrivateJobResult(t.Context(), persisted, allowPrivateFixture)
	if err != nil || !bytes.Equal(raw, syntheticPrivate) {
		t.Fatal("restart lost private handoff", err)
	}
	page, err := s.Jobs(t.Context(), "speech", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(page)
	if bytes.Contains(public, []byte("private-fixture")) || bytes.Contains(public, []byte("18446744073709551615")) {
		t.Fatal("private payload escaped task listing")
	}
	raw[0] = 'x'
	again, err := s.PrivateJobResult(t.Context(), persisted, allowPrivateFixture)
	if err != nil || !bytes.Equal(again, syntheticPrivate) {
		t.Fatal("caller modified persisted payload", err)
	}
}

func TestPrivateResultWriteFailureRollsBackCompletion(t *testing.T) {
	s, j, _ := privateResultFixture(t)
	if _, err := s.db.Exec(`CREATE TRIGGER synthetic_write_failure BEFORE INSERT ON job_private_results BEGIN SELECT RAISE(ABORT,'synthetic disk write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteJobWithPrivate(t.Context(), j.ID, j.Revision, nil, json.RawMessage(`{"ok":true}`), syntheticPrivate); err == nil {
		t.Fatal("write failure ignored")
	}
	current, err := s.Job(t.Context(), j.ID)
	if err != nil || current.State != "submitting" || current.Revision != j.Revision || len(current.Result) != 0 {
		t.Fatal("partial completion committed", current.State, err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM job_private_results`).Scan(&count); err != nil || count != 0 {
		t.Fatal("orphan private record", count, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER synthetic_write_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteJobWithPrivate(t.Context(), j.ID, j.Revision, nil, json.RawMessage(`{"ok":true}`), syntheticPrivate); err != nil {
		t.Fatal("checkpoint cannot resume", err)
	}
}

func TestPrivateResultRejectsChangedScopeSourceAndInstallation(t *testing.T) {
	for _, kind := range []string{"account", "thread", "source", "revision", "disabled", "removed", "grant", "version", "config", "cancelled", "nil-check", "revoked-before", "revoked-after"} {
		t.Run(kind, func(t *testing.T) {
			s, j, _ := privateResultFixture(t)
			if kind == "cancelled" {
				var err error
				j, err = s.RequestJobCancel(t.Context(), j.ID, j.Revision)
				if err != nil {
					t.Fatal(err)
				}
			}
			finished, err := s.CompleteJobWithPrivate(t.Context(), j.ID, j.Revision, nil, json.RawMessage(`{"ok":true}`), syntheticPrivate)
			if err != nil {
				t.Fatal(err)
			}
			check := allowPrivateFixture
			switch kind {
			case "account":
				finished.Context.Account = "other"
			case "thread":
				finished.Context.Thread = "other"
			case "source":
				finished.Context.SourceVersions["source"] = 2
			case "revision":
				finished.Revision++
			case "disabled":
				_, err = s.db.Exec(`UPDATE installed SET enabled=0`)
			case "removed":
				_, err = s.db.Exec(`UPDATE installed SET removed=1`)
			case "grant":
				_, err = s.db.Exec(`UPDATE installed SET permissions='[]'`)
			case "version":
				_, err = s.db.Exec(`UPDATE installed SET digest='changed-package'`)
			case "config":
				if _, err = s.db.Exec(`UPDATE installed SET enabled=0`); err != nil {
					t.Fatal(err)
				}
				_, err = s.SaveConfiguration(t.Context(), "speech", ConfigUpdate{})
				if err == nil {
					_, err = s.db.Exec(`UPDATE installed SET enabled=1`)
				}
			case "nil-check":
				check = nil
			case "revoked-before", "revoked-after":
				calls := 0
				check = func(context.Context, Job) error {
					calls++
					if kind == "revoked-before" || calls == 2 {
						return errors.New("source forgotten")
					}
					return nil
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if raw, err := s.PrivateJobResult(t.Context(), finished, check); err == nil || len(raw) != 0 {
				t.Fatal("changed authority exposed private handoff")
			}
		})
	}
}
