package companion

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSourceInstancePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s, e := NewStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	id := s.SourceInstanceID()
	if len(id) != 48 {
		t.Fatal(id)
	}
	s.Close()
	s, e = NewStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if id != s.SourceInstanceID() {
		t.Fatal("source namespace changed")
	}
}

func TestLegacyRowIDDoesNotQuarantineFreshNativeDataset(t *testing.T) {
	for _, oldMarked := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "legacy_quarantined"}[oldMarked], func(t *testing.T) {
			s, c, in := memoryFixture(t)
			now := time.Now().UTC().Truncate(time.Second)
			in.TraceID = "reused-local-row-91"
			in.Sender = "old-person"
			in.Text = "old imported request"
			in.MessageTime = now.Add(-24 * time.Hour).Format(time.RFC3339)
			if err := s.Observe(in, c, now); err != nil {
				t.Fatal(err)
			}
			if oldMarked {
				var a SourceAudit
				if err := s.db.QueryRow("SELECT id,scope,trace,revision,hash FROM events WHERE trace=?", in.TraceID).Scan(&a.EventID, &a.Scope, &a.Trace, &a.Revision, &a.Hash); err != nil {
					t.Fatal(err)
				}
				if err := s.QuarantineSources(t.Context(), []SourceAudit{a}); err != nil {
					t.Fatal(err)
				}
			}
			in.Sender = "new-person"
			in.Text = "actual video observation"
			empty := ""
			in.OriginalText = &empty
			in.MessageTime = now.Format(time.RFC3339)
			in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID, ServerID: "new-server", OccurredAt: in.MessageTime, MessageType: 49}
			for i := 0; i < 2; i++ {
				if err := s.Observe(in, c, now); err != nil {
					t.Fatal(err)
				}
			}
			r, ok := s.SourceForInput(in)
			if !ok || r.Integrity == "conflicted" || r.Sender != in.Sender {
				t.Fatal("fresh share blocked by legacy row", r)
			}
			var n int
			if err := s.db.QueryRow("SELECT count(*) FROM events WHERE trace=?", in.TraceID).Scan(&n); err != nil || n != 2 {
				t.Fatal("lost original or duplicated retry", n, err)
			}
		})
	}
}
func TestSourceCollisionPreservesBothEventsAndIsIdempotent(t *testing.T) {
	s, c, in := memoryFixture(t)
	now := time.Now()
	in.MessageTime = now.Add(-time.Hour).UTC().Format(time.RFC3339)
	in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: "local-57", ServerID: "old-server", OccurredAt: in.MessageTime}
	in.TraceID = "local-57"
	in.Text = "old observation"
	if e := s.Observe(in, c, now); e != nil {
		t.Fatal(e)
	}
	in.MessageTime = now.UTC().Format(time.RFC3339)
	in.Source.ServerID = "new-server"
	in.Source.OccurredAt = in.MessageTime
	in.Text = "new observation"
	if e := s.Observe(in, c, now); e != nil {
		t.Fatal(e)
	}
	if e := s.Observe(in, c, now); e != nil {
		t.Fatal(e)
	}
	var n int
	s.db.QueryRow("SELECT count(*) FROM events WHERE trace=?", in.TraceID).Scan(&n)
	if n != 2 {
		t.Fatal("source overwritten or replay appended", n)
	}
	if got := s.Context(in, c, now); len(got.Messages) != 0 {
		t.Fatal("conflicted source in context", got.Messages)
	}
	audit, e := AuditSources(context.Background(), filepath.Dir(s.path))
	if e != nil || len(audit) != 2 {
		t.Fatal(audit, e)
	}
}
func TestSourceRevisionRequiresExplicitVersion(t *testing.T) {
	s, c, in := memoryFixture(t)
	now := time.Now()
	in.TraceID = "source-revision"
	in.MessageTime = now.UTC().Format(time.RFC3339)
	in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID, OccurredAt: in.MessageTime, Revision: 1}
	in.Text = "first"
	if e := s.Observe(in, c, now); e != nil {
		t.Fatal(e)
	}
	in.Text = "corrected"
	in.Source.Revision = 2
	if e := s.Observe(in, c, now); e != nil {
		t.Fatal(e)
	}
	var n, rev int
	s.db.QueryRow("SELECT count(*),max(revision) FROM events WHERE trace=?", in.TraceID).Scan(&n, &rev)
	if n != 1 || rev != 2 {
		t.Fatal(n, rev)
	}
}
func TestQuarantineSourceExpectedVersionAndHash(t *testing.T) {
	s, c, in := memoryFixture(t)
	now := time.Now()
	in.TraceID = "quarantine-source"
	in.Text = "preserve original"
	if e := s.Observe(in, c, now); e != nil {
		t.Fatal(e)
	}
	var a SourceAudit
	if e := s.db.QueryRow("SELECT id,scope,trace,revision,hash FROM events WHERE trace=?", in.TraceID).Scan(&a.EventID, &a.Scope, &a.Trace, &a.Revision, &a.Hash); e != nil {
		t.Fatal(e)
	}
	wrong := a
	wrong.Revision++
	if e := s.QuarantineSources(t.Context(), []SourceAudit{wrong}); e == nil {
		t.Fatal("stale quarantine accepted")
	}
	if e := s.QuarantineSources(t.Context(), []SourceAudit{a}); e != nil {
		t.Fatal(e)
	}
	var text string
	s.db.QueryRow("SELECT text FROM events WHERE id=?", a.EventID).Scan(&text)
	if text != in.Text {
		t.Fatal("original changed")
	}
	if got := s.Context(in, c, now); len(got.Messages) != 0 {
		t.Fatal("quarantined source still active")
	}
}
func TestSourceMetadataDeepCopy(t *testing.T) {
	sc := Scope{Messages: []Turn{{Source: &SourceRef{Sender: "a"}, Addressees: []string{"b"}, ReplyContext: &ReplyContext{TargetIDs: []string{"b"}}, IncomingMedia: []IncomingMediaRef{{UploaderID: "a"}}}}}
	copy := copyScope(sc)
	copy.Messages[0].Source.Sender = "x"
	copy.Messages[0].Addressees[0] = "x"
	copy.Messages[0].ReplyContext.TargetIDs[0] = "x"
	copy.Messages[0].IncomingMedia[0].UploaderID = "x"
	if sc.Messages[0].Source.Sender != "a" || sc.Messages[0].Addressees[0] != "b" || sc.Messages[0].ReplyContext.TargetIDs[0] != "b" || sc.Messages[0].IncomingMedia[0].UploaderID != "a" {
		t.Fatal("shared metadata mutated")
	}
}

func TestSourceObservationRefreshPreservesUserOriginal(t *testing.T) {
	s, c, in := memoryFixture(t)
	now := time.Now()
	in.TraceID = "image-source"
	in.MessageTime = now.UTC().Format(time.RFC3339)
	empty := ""
	in.OriginalText = &empty
	in.Text = "old image observation"
	in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID, OccurredAt: in.MessageTime}
	if err := s.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	if _, err := s.UpdateObservation(scope, in.TraceID, in.Text, "new image observation"); err != nil {
		t.Fatal(err)
	}
	var body string
	var rev int
	if err := s.db.QueryRow("SELECT text,revision FROM events WHERE trace=?", in.TraceID).Scan(&body, &rev); err != nil {
		t.Fatal(err)
	}
	if body != "" || rev != 1 {
		t.Fatal("observation promoted to original", body, rev)
	}
}

func TestSourceReplayUsesSourceIdentityNotExecutionTrace(t *testing.T) {
	s, c, in := memoryFixture(t)
	now := time.Now()
	in.TraceID = "exec-first"
	in.MessageTime = now.UTC().Format(time.RFC3339)
	in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: "native-identity", OccurredAt: in.MessageTime}
	in.Text = "same real message"
	if e := s.Observe(in, c, now); e != nil {
		t.Fatal(e)
	}
	first, ok := s.SourceForInput(in)
	if !ok {
		t.Fatal("source not resolved")
	}
	in.TraceID = "exec-retry"
	if e := s.Observe(in, c, now.Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	got := s.Context(in, c, now.Add(time.Second))
	if len(got.Messages) != 1 {
		t.Fatal("trace retry duplicated event", len(got.Messages))
	}
	second, ok := s.SourceForInput(in)
	if !ok || second.EventID != first.EventID || second.ReceivedAt != first.ReceivedAt {
		t.Fatal("source identity/first seen changed", second, first)
	}
}

func TestReplyContextSourceVersionsAreDeepCopied(t *testing.T) {
	original := ReplyContext{SourceVersions: map[string]int{"source": 2}}
	copy := cloneReplyContext(&original)
	copy.SourceVersions["source"] = 5
	delete(copy.SourceVersions, "source")
	if original.SourceVersions["source"] != 2 {
		t.Fatal("cloned source versions changed original")
	}
	scope := copyScope(Scope{Messages: []Turn{{ReplyContext: &original}}})
	scope.Messages[0].ReplyContext.SourceVersions["source"] = 7
	if original.SourceVersions["source"] != 2 {
		t.Fatal("scope copy shared source versions")
	}
}
func TestSourceForInputDoesNotResolveForgottenOrSuppressedEvents(t *testing.T) {
	for _, kind := range []string{"forgotten", "suppressed"} {
		t.Run(kind, func(t *testing.T) {
			s, c, in := memoryFixture(t)
			now := time.Now()
			in.TraceID = "blocked-source"
			in.MessageTime = now.UTC().Format(time.RFC3339)
			in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID, OccurredAt: in.MessageTime}
			in.Text = "same source"
			if err := s.Observe(in, c, now); err != nil {
				t.Fatal(err)
			}
			source, ok := s.SourceForInput(in)
			if !ok {
				t.Fatal("initial source missing")
			}
			if kind == "forgotten" {
				s.db.Exec("INSERT INTO memory_blocks VALUES(?,?)", ScopeID(in.Channel, in.Account, in.Thread), source.EventID)
			} else {
				s.db.Exec("UPDATE events SET suppressed=1 WHERE id=?", source.EventID)
			}
			if _, ok = s.SourceForInput(in); ok {
				t.Fatal("blocked source resolved")
			}
			if got := s.Context(in, c, now); len(got.Messages) != 0 {
				t.Fatal("blocked source visible")
			}
			in.Source.InstanceID = "replacement-import-instance"
			if err := s.Observe(in, c, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, ok = s.SourceForInput(in); ok {
				t.Fatal("namespace change revived forgotten source")
			}
			if got := s.Context(in, c, now.Add(time.Second)); len(got.Messages) != 0 {
				t.Fatal("namespace change revived blocked context")
			}
		})
	}
}
func TestSourceReceivedAtIsFirstObservationAndTimeMustAgree(t *testing.T) {
	s, c, in := memoryFixture(t)
	now := time.Now()
	in.TraceID = "clock-source"
	in.MessageTime = now.Add(-time.Hour).UTC().Format(time.RFC3339)
	in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID, OccurredAt: in.MessageTime, ReceivedAt: now.Add(-24 * time.Hour).Format(time.RFC3339)}
	in.Text = "clock"
	if err := s.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	got, ok := s.SourceForInput(in)
	if !ok || got.ReceivedAt != memoryTime(now) {
		t.Fatal("first observed time came from input", got)
	}
	in.Source.OccurredAt = now.Format(time.RFC3339)
	if err := s.Observe(in, c, now); err == nil {
		t.Fatal("contradictory occurrence time accepted")
	}
}

func TestCloneIncomingMediaDeepCopiesObservationParts(t *testing.T) {
	original := []IncomingMediaRef{{ObservationParts: []ObservationPart{{Kind: ObservationVisualFrames, Range: "frame 1", Excerpt: "海面"}}}}
	cloned := cloneIncomingMedia(original)
	cloned[0].ObservationParts[0].Excerpt = "已修改"
	if original[0].ObservationParts[0].Excerpt != "海面" {
		t.Fatal("observation parts were shallow-copied")
	}
}
