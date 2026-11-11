package companion

import (
	"testing"
	"time"
)

func TestLocalRequestSourceAndEvidenceSurviveRecentPruning(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := Defaults()
	cfg.Permissions.Remember = true
	cfg.MemoryMessages = 1
	in := localTestInput("archived-request", "合成旧请求", time.Now().UTC())
	if err := store.Observe(in, cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	run, err := store.OpenLocalToolRun(t.Context(), in, true)
	if err != nil {
		t.Fatal(err)
	}
	op := LocalToolCheckpoint{Tool: "synthetic-metadata", Key: "proof-key", Grant: "synthetic-grant", Target: "source"}
	if err := run.Reserve(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordResult(t.Context(), op.Key, "synthetic-version"); err != nil {
		t.Fatal(err)
	}
	source, err := store.LocalRequestSource(t.Context(), in, in.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	other := localTestInput("new-request", "合成新请求", time.Now().UTC())
	if err := store.Observe(other, cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, recent := store.SourceForInput(in); recent {
		t.Fatal("fixture did not prune original")
	}
	resolved, err := store.LocalRequestSource(t.Context(), in, in.TraceID)
	if err != nil || *resolved != *source {
		t.Fatal("durable original not resolved", err)
	}
	in.Source = source
	proofs, err := store.LocalToolEvidence(t.Context(), in, op.Tool)
	if err != nil || len(proofs) != 1 || proofs[0].Result != "synthetic-version" {
		t.Fatal(proofs, err)
	}
	forged := in
	forged.Account = "other-fish"
	if _, err := store.LocalToolEvidence(t.Context(), forged, op.Tool); err == nil {
		t.Fatal("foreign scope used canonical source")
	}
	source.Revision++
	if _, err := store.LocalToolEvidence(t.Context(), in, op.Tool); err == nil {
		t.Fatal("changed source revision accepted")
	}
	if _, err := store.db.Exec("UPDATE events SET suppressed=1 WHERE trace=?", in.TraceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LocalRequestSource(t.Context(), in, in.TraceID); err == nil {
		t.Fatal("suppressed original resolved")
	}
}
