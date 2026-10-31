package pluginhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func installConfigFixture(t *testing.T, s *Store) InstalledPackage {
	t.Helper()
	dir := readyFixture(t)
	schema := []byte(`{"type":"object","additionalProperties":false,"required":["backend"],"properties":{"backend":{"type":"string","enum":["synthetic"],"default":"synthetic"},"key":{"type":"string","x-secret":true},"count":{"type":"integer","minimum":1,"maximum":100,"default":5}}}`)
	os.WriteFile(filepath.Join(dir, "config.schema.json"), schema, 0600)
	raw, _ := os.ReadFile(filepath.Join(dir, "plugin.json"))
	var p Package
	json.Unmarshal(raw, &p)
	sum := sha256.Sum256(schema)
	p.Files["config.schema.json"] = hex.EncodeToString(sum[:])
	raw, _ = json.Marshal(p)
	os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0600)
	prepared, err := s.Prepare(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := s.Install(context.Background(), prepared.Token, prepared.Digest, p.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	return installed
}
func TestConfigurationRevisionSecretsAndValidation(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(t.TempDir(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	installConfigFixture(t, s)
	c, err := s.SaveConfiguration(ctx, "speech", ConfigUpdate{Secrets: map[string]string{"key": "synthetic-private-key"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Revision != 1 || len(c.ConfiguredSecrets) != 1 || string(c.Values["count"]) != "5" {
		t.Fatalf("configuration %+v", c)
	}
	raw, _ := json.Marshal(c)
	if bytes.Contains(raw, []byte("synthetic-private-key")) {
		t.Fatal("secret in response")
	}
	if _, err = s.SaveConfiguration(ctx, "speech", ConfigUpdate{}); !errors.Is(err, ErrRevision) {
		t.Fatal("stale write accepted", err)
	}
	if _, err = s.SaveConfiguration(ctx, "speech", ConfigUpdate{Revision: 1, Values: map[string]json.RawMessage{"count": json.RawMessage(`0`)}}); err == nil {
		t.Fatal("invalid value accepted")
	}
	if _, err = s.SaveConfiguration(ctx, "speech", ConfigUpdate{Revision: 1, Values: map[string]json.RawMessage{"key": json.RawMessage(`"public-leak"`)}}); err == nil {
		t.Fatal("secret in public values accepted")
	}
	c, err = s.SaveConfiguration(ctx, "speech", ConfigUpdate{Revision: 1, Secrets: map[string]string{"key": ""}})
	if err != nil || len(c.ConfiguredSecrets) != 1 {
		t.Fatal("blank overwrote key", err)
	}
	c, err = s.SaveConfiguration(ctx, "speech", ConfigUpdate{Revision: 2, ClearSecrets: []string{"key"}})
	if err != nil || len(c.ConfiguredSecrets) != 0 {
		t.Fatal("explicit clear failed", err)
	}
}
func TestDurableJobIdentityClaimCancelAndPagination(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := OpenStore(dir, "a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	installed := installConfigFixture(t, s)
	// Internal fixture simulates the later controlled runtime's verified enable.
	if _, err = s.db.Exec(`UPDATE installed SET enabled=1 WHERE id='speech'`); err != nil {
		t.Fatal(err)
	}
	input := Job{Plugin: "speech", Version: installed.Package.Version, PackageDigest: installed.Digest, ConfigRevision: "0", Tool: "synthesize", Context: Invocation{Account: "a", Thread: "t", Requester: "u", Purpose: "offline", RequestKey: "first", SourceVersions: map[string]int{"source": 1}}, Arguments: json.RawMessage(`{"large":9007199254740993,"text":"synthetic"}`)}
	first, created, err := s.CreateJob(ctx, input)
	if err != nil || !created {
		t.Fatal(err)
	}
	input.Arguments = json.RawMessage(`{"text":"synthetic","large":9007199254740993}`)
	retry, created, err := s.CreateJob(ctx, input)
	if err != nil || created || retry.ID != first.ID {
		t.Fatal("retry not idempotent", err)
	}
	input.Context.SourceVersions["source"] = 2
	if _, _, err = s.CreateJob(ctx, input); !errors.Is(err, ErrRequestConflict) {
		t.Fatal("changed evidence accepted", err)
	}
	input.Context.SourceVersions["source"] = 1
	claimed, err := s.ClaimJob(ctx, first.ID, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimJob(ctx, first.ID, first.Revision); !errors.Is(err, ErrRevision) {
		t.Fatal("double claim", err)
	}
	s.Close()
	s, err = OpenStore(dir, "a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	persisted, err := s.Job(ctx, first.ID)
	if err != nil || persisted.Recovery() != "needs_review" || !bytes.Contains(persisted.Arguments, []byte("9007199254740993")) {
		t.Fatal("restart lost request", err)
	}
	running, err := s.AcknowledgeJob(ctx, first.ID, claimed.Revision, "remote-1")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.RequestJobCancel(ctx, first.ID, running.Revision)
	if err != nil || cancelled.State != "running" || !cancelled.CancelRequested || cancelled.Recovery() != "query" {
		t.Fatal("cancel falsely terminal", err)
	}
	finished, err := s.FinishJob(ctx, first.ID, cancelled.Revision, "cancelled", nil)
	if err != nil || finished.State != "cancelled" {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		input.Context.RequestKey = "next-" + strconv.Itoa(i)
		if _, _, err = s.CreateJob(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.Jobs(ctx, "speech", 0, 2)
	if err != nil || len(page.Items) != 2 || !page.HasMore {
		t.Fatal("page 1", err)
	}
	next, err := s.Jobs(ctx, "speech", page.Next, 2)
	if err != nil || len(next.Items) != 2 || next.HasMore || next.Items[0].ID == page.Items[0].ID {
		t.Fatal("page 2", err)
	}
}
