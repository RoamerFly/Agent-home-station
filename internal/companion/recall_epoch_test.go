package companion

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func recallEpochForTest(t *testing.T, s *Store) string {
	t.Helper()
	v, err := s.RecallEpoch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRecallEpochIndependentMessagesDoNotInvalidate(t *testing.T) {
	s, c, in := memoryFixture(t)
	if got := recallEpochForTest(t, s); got != "0" {
		t.Fatal("missing legacy/new meta must read zero", got)
	}
	before := recallEpochForTest(t, s)
	dataBefore, err := s.DataEpoch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		in.TraceID = fmt.Sprintf("independent-%d", i)
		in.Text = fmt.Sprintf("a new request %d", i)
		in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID}
		if err = s.Observe(in, c, time.Now()); err != nil {
			t.Fatal(err)
		}
		if got := recallEpochForTest(t, s); got != before {
			t.Fatal("independent message invalidated existing evidence", before, got)
		}
	}
	in.TraceID = "legacy-independent"
	in.Source = nil
	in.Sender = "bob"
	in.Text = "another member's independent message"
	if err = s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = s.Accepted(in, "accepted answer", c, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = s.AcceptedDelivery(in, "accepted delivery", "delivery-id", c, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := recallEpochForTest(t, s); got != before {
		t.Fatal("assistant append invalidated evidence", before, got)
	}
	if after, e := s.DataEpoch(t.Context()); e != nil || after == dataBefore {
		t.Fatal("snapshot epoch did not reflect append", dataBefore, after, e)
	}
	dir := filepath.Dir(s.path)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := recallEpochForTest(t, reopened); got != before {
		t.Fatal("restart changed evidence epoch", got)
	}
}

func TestRecallEpochSourceRevisionConflictAndClear(t *testing.T) {
	for _, action := range []string{"revision", "collision", "observation", "clear", "summary", "notes", "quarantine"} {
		t.Run(action, func(t *testing.T) {
			s, c, in := memoryFixture(t)
			now := time.Now().UTC()
			in.Text = "first observation"
			in.MessageTime = now.Format(time.RFC3339Nano)
			in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID, Revision: 1, OccurredAt: in.MessageTime}
			if err := s.Observe(in, c, now); err != nil {
				t.Fatal(err)
			}
			before := recallEpochForTest(t, s)
			id := ScopeID(in.Channel, in.Account, in.Thread)
			var err error
			switch action {
			case "revision", "collision":
				in.Text = "a revised observation"
				if action == "revision" {
					in.Source.Revision = 2
				}
				err = s.Observe(in, c, now)
			case "observation":
				_, err = s.UpdateObservation(id, in.TraceID, in.Text, "new media interpretation")
			case "clear":
				err = s.Clear(id)
			case "summary", "notes":
				err = s.scopeTransaction(id, func(data *persisted) error {
					sc := data.Scopes[id]
					if action == "summary" {
						sc.Summary = "updated source summary"
					} else {
						sc.Notes = []string{"verified relationship update"}
					}
					data.Scopes[id] = sc
					return nil
				})
			case "quarantine":
				var event, hash string
				var revision int
				if err = s.db.QueryRow("SELECT id,hash,revision FROM events WHERE scope=?", id).Scan(&event, &hash, &revision); err != nil {
					t.Fatal(err)
				}
				err = s.QuarantineSources(t.Context(), []SourceAudit{{EventID: event, Scope: id, Revision: revision, Hash: hash}})
			}
			if err != nil {
				t.Fatal(err)
			}
			if after := recallEpochForTest(t, s); after == before {
				t.Fatal("changed evidence retained old epoch", action, before)
			}
		})
	}
}

func TestRecallEpochMemoryChangesAndFailedTransaction(t *testing.T) {
	s, m, _ := relationFixture(t, true)
	before := recallEpochForTest(t, s)
	saved, err := s.SaveMemory(t.Context(), m, 0)
	if err != nil {
		t.Fatal(err)
	}
	if recallEpochForTest(t, s) == before {
		t.Fatal("new long-term assertion failed to advance epoch")
	}
	before = recallEpochForTest(t, s)
	saved.Detail = "corrected fact description"
	saved, err = s.SaveMemory(t.Context(), saved, saved.Version)
	if err != nil {
		t.Fatal(err)
	}
	if recallEpochForTest(t, s) == before {
		t.Fatal("memory correction failed to advance epoch")
	}
	before = recallEpochForTest(t, s)
	if _, err = s.SaveMemory(t.Context(), saved, saved.Version-1); err == nil {
		t.Fatal("stale revision accepted")
	}
	if recallEpochForTest(t, s) != before {
		t.Fatal("failed transaction published epoch")
	}
	saved.Status = "deleted"
	if _, err = s.SaveMemory(t.Context(), saved, saved.Version); err != nil {
		t.Fatal(err)
	}
	if recallEpochForTest(t, s) == before {
		t.Fatal("forget/block failed to advance epoch")
	}
}

func TestRecallEpochReplacementPersistsAcrossRestart(t *testing.T) {
	s, c, in := memoryFixture(t)
	in.Text = "我喜欢红茶"
	if err := s.Observe(in, c, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	id := ScopeID(in.Channel, in.Account, in.Thread)
	p, err := s.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{id}, Originals: true})
	if err != nil || len(p.Items) != 1 {
		t.Fatal(p, err)
	}
	old, err := s.SaveMemory(t.Context(), MemoryEntry{Scope: id, Subject: in.Sender, Kind: "preference", Detail: "喜欢红茶", Sources: []MemorySource{{p.Items[0].Event.ID, in.Text}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	in.TraceID = "explicit-correction"
	in.Text = "更正，我现在喜欢绿茶"
	if err = s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	p, err = s.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{id}, Query: "绿茶", Originals: true})
	if err != nil {
		t.Fatal(p, err)
	}
	var correctedID string
	for _, item := range p.Items {
		if item.Event != nil && item.Event.Trace == in.TraceID {
			correctedID = item.Event.ID
		}
	}
	if correctedID == "" {
		t.Fatal("corrected source absent")
	}
	before := recallEpochForTest(t, s)
	if _, err = s.SaveMemory(t.Context(), MemoryEntry{Scope: id, Subject: in.Sender, Kind: "preference", Detail: "现在喜欢绿茶", Supersedes: old.ID, Sources: []MemorySource{{correctedID, in.Text}}}, 0); err != nil {
		t.Fatal(err)
	}
	after := recallEpochForTest(t, s)
	if after == before {
		t.Fatal("superseding entry retained old epoch")
	}
	dir := filepath.Dir(s.path)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := recallEpochForTest(t, reopened); got != after {
		t.Fatal("committed recall epoch lost on restart", after, got)
	}
}

func TestRecallEpochAdminCorrectionAndAtomicRollback(t *testing.T) {
	s, m, ev := relationFixture(t, false)
	before := recallEpochForTest(t, s)
	a := MemoryAssertion{Subject: "bob", Predicate: "father", Object: "relative:bob:father", SubjectBasis: "speaker", SubjectText: "我", ObjectBasis: "relative", ObjectText: "爸", PredicateText: "爸"}
	if _, err := s.SaveSourcedAdminCorrection(t.Context(), m.Scope, "bob", ev.ID, ev.Revision, "这句话对我说，你爸指我的爸爸", []MemoryAssertion{a}); err != nil {
		t.Fatal(err)
	}
	if recallEpochForTest(t, s) == before {
		t.Fatal("admin correction failed to advance epoch")
	}
	before = recallEpochForTest(t, s)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = bumpRecallEpoch(tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if recallEpochForTest(t, s) != before {
		t.Fatal("rollback leaked evidence epoch")
	}
}
