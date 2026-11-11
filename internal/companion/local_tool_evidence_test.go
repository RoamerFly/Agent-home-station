package companion

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLocalToolEvidenceExactSourceFilterAndRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	cfg := Defaults()
	cfg.Permissions.Remember = true
	in := localTestInput("evidence-one", "synthetic original", time.Now().UTC())
	if err := s.Observe(in, cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	run, err := s.OpenLocalToolRun(t.Context(), in, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []LocalToolCheckpoint{{Key: "one", Tool: "app_inbox", Grant: "opaque-grant", Target: "douyin:notifications"}, {Key: "two", Tool: "another_tool", Grant: "not-selected", Target: "private-target"}} {
		if err := run.Reserve(t.Context(), op); err != nil {
			t.Fatal(err)
		}
		if err := run.RecordResult(t.Context(), op.Key, `{"body_retained":false}`); err != nil {
			t.Fatal(err)
		}
	}
	in.Source, _ = s.SourceForInput(in)
	if in.Source == nil {
		t.Fatal("missing canonical source")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.LocalToolEvidence(t.Context(), in, "app_inbox")
	if err != nil || len(rows) != 1 || rows[0].Key != "one" || rows[0].Grant != "opaque-grant" {
		t.Fatalf("evidence after restart: %+v %v", rows, err)
	}
	rows, err = s.LocalToolEvidence(t.Context(), in, "missing-tool")
	if err != nil || len(rows) != 0 {
		t.Fatalf("empty source mistaken for unavailable: %+v %v", rows, err)
	}
	for _, change := range []func(*Input){
		func(i *Input) { i.Account = "another-fish" },
		func(i *Input) { i.Thread = "another-owner"; i.Sender = "another-owner" },
		func(i *Input) { i.Source.InstanceID = "another-database" },
		func(i *Input) { i.Source.Revision++ },
		func(i *Input) { i.Source.EventID = "another-event" },
	} {
		bad := in
		source := *in.Source
		bad.Source = &source
		change(&bad)
		if rows, err := s.LocalToolEvidence(t.Context(), bad, "app_inbox"); err == nil || len(rows) != 0 {
			t.Fatalf("cross-source metadata: %+v %v", rows, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.LocalToolEvidence(ctx, in, "app_inbox"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if err := s.Clear(ScopeID(LocalChannel, in.Account, in.Thread)); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.LocalToolEvidence(t.Context(), in, "app_inbox"); err == nil || len(rows) != 0 {
		t.Fatalf("forgotten metadata: %+v %v", rows, err)
	}
}
