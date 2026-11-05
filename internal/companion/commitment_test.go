package companion

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func commitmentEvent(t *testing.T, s *Store, c Config, in Input, at time.Time) MemoryEvent {
	t.Helper()
	if err := s.Observe(in, c, at); err != nil {
		t.Fatal(err)
	}
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	page, err := s.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{scope}, Originals: true, Query: in.Text, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range page.Items {
		if hit.Event != nil && hit.Event.Trace == in.TraceID {
			return *hit.Event
		}
	}
	t.Fatalf("event %q was not stored", in.TraceID)
	return MemoryEvent{}
}

func TestCommitmentCorrectionRetainsSourceChainAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	c := Defaults()
	c.Permissions.Remember = true
	ctx := context.Background()
	in := Input{Channel: "wechat", Account: "synthetic", Thread: "xiaolin", Sender: "xiaolin"}
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	dayOne := time.Now().UTC().Add(-48 * time.Hour)
	in.TraceID, in.Text = "xiaolin-saturday", "我答应你周六画一张海边明信片。"
	first := commitmentEvent(t, s, c, in, dayOne)
	promise, err := s.SaveCommitment(ctx, MemoryEntry{
		Scope: scope, Subject: in.Sender, Detail: "小林答应周六画一张海边明信片", Summary: "周六画海边明信片",
		Sources: []MemorySource{{EventID: first.ID, Quote: in.Text}}, CommitmentState: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	in.TraceID, in.Text = "xiaolin-sunday", "更正一下，改到周日画那张海边明信片。"
	second := commitmentEvent(t, s, c, in, dayOne.Add(24*time.Hour))
	corrected, err := s.SaveCommitment(ctx, MemoryEntry{
		Scope: scope, Subject: in.Sender, Detail: "小林改为周日画一张海边明信片", Summary: "周日画海边明信片",
		Sources: []MemorySource{{EventID: second.ID, Quote: in.Text}}, CommitmentState: "open", Corrects: promise.ID, CorrectsVersion: promise.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if corrected.ID != promise.ID || corrected.Version != promise.Version+1 || len(corrected.Sources) != 2 {
		t.Fatalf("correction did not update one source-backed agreement: %#v", corrected)
	}
	if corrected.Sources[0].EventID != first.ID || corrected.Sources[1].EventID != second.ID {
		t.Fatalf("source order/chain lost: %#v", corrected.Sources)
	}
	replay, err := s.SaveCommitment(ctx, MemoryEntry{
		Scope: scope, Subject: in.Sender, Detail: corrected.Detail, Summary: corrected.Summary,
		Sources: []MemorySource{{EventID: second.ID, Quote: in.Text}}, CommitmentState: "open", Corrects: promise.ID, CorrectsVersion: promise.Version,
	})
	if err != nil || replay.Version != corrected.Version {
		t.Fatalf("interrupted extraction replay was not idempotent: %#v %v", replay, err)
	}
	_, history, err := s.MemoryHistory(ctx, []string{scope}, promise.ID)
	if err != nil || len(history) != 1 || len(history[0].Sources) != 1 || history[0].Sources[0].EventID != first.ID {
		t.Fatalf("prior agreement not auditable: %#v %v", history, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	afterRestart, err := s.ReadMemory(ctx, []string{scope}, promise.ID)
	if err != nil || afterRestart.Detail != corrected.Detail || afterRestart.CommitmentState != "open" || len(afterRestart.Sources) != 2 {
		t.Fatalf("restart lost corrected commitment: %#v %v", afterRestart, err)
	}
}

func TestCommitmentCancelDoesNotForgetOrReviveOtherAgreements(t *testing.T) {
	s, c, in := memoryFixture(t)
	ctx := context.Background()
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	base := time.Now().UTC().Add(-3 * time.Hour)
	in.TraceID, in.Text = "first-promise", "我明天会把照片发给你。"
	first := commitmentEvent(t, s, c, in, base)
	promise, err := s.SaveCommitment(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "明天发照片", Sources: []MemorySource{{EventID: first.ID, Quote: in.Text}}})
	if err != nil {
		t.Fatal(err)
	}
	in.TraceID, in.Text = "cancel-promise", "刚才那件事取消，不发照片了。"
	cancel := commitmentEvent(t, s, c, in, base.Add(time.Hour))
	cancelled, err := s.SaveCommitment(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "已取消发照片的约定", Sources: []MemorySource{{EventID: cancel.ID, Quote: in.Text}}, CommitmentState: "cancelled", Corrects: promise.ID, CorrectsVersion: promise.Version})
	if err != nil || cancelled.Status != "active" || cancelled.CommitmentState != "cancelled" || cancelled.Pinned || len(cancelled.Sources) != 2 {
		t.Fatalf("cancel became forget or lost trace: %#v %v", cancelled, err)
	}
	if _, err = s.ReadEvent(ctx, []string{scope}, first.ID); err != nil {
		t.Fatalf("cancelling an agreement forgot its source: %v", err)
	}
	pinned, err := s.SearchMemory(ctx, MemoryQuery{Scopes: []string{scope}, Pinned: true, Query: "照片"})
	if err != nil || len(pinned.Items) != 0 {
		t.Fatalf("cancelled agreement remained current: %#v %v", pinned, err)
	}
	in.TraceID, in.Text = "other-promise", "我明天会把书发给你。"
	other := commitmentEvent(t, s, c, in, base.Add(2*time.Hour))
	otherPromise, err := s.SaveCommitment(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "明天发书", Sources: []MemorySource{{EventID: other.ID, Quote: in.Text}}})
	if err != nil || otherPromise.ID == promise.ID || !otherPromise.Pinned {
		t.Fatalf("unrelated promise was coupled to cancellation: %#v %v", otherPromise, err)
	}
	// Explicit privacy forgetting still blocks later retries from using the old
	// sources; it is intentionally distinct from the preceding cancellation.
	cancelled.Status = "deleted"
	if _, err = s.SaveMemory(ctx, cancelled, cancelled.Version); err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveCommitment(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "错误地尝试恢复旧照片约定", Sources: []MemorySource{{EventID: first.ID, Quote: "我明天会把照片发给你。"}}, Corrects: promise.ID, CorrectsVersion: promise.Version})
	if err == nil || !errors.Is(err, ErrMemoryConflict) && err.Error() != "sql: no rows in result set" {
		t.Fatalf("forgotten commitment was unexpectedly revived: %v", err)
	}
}

func TestCommitmentSameSourceCanDescribeMultiplePromisesAndRejectStaleCAS(t *testing.T) {
	s, c, in := memoryFixture(t)
	ctx := context.Background()
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	in.TraceID, in.Text = "two-promises", "明天我给你发照片，也把书带过去。"
	ev := commitmentEvent(t, s, c, in, time.Now().UTC().Add(-time.Hour))
	photo, err := s.SaveCommitment(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "明天发照片", Sources: []MemorySource{{EventID: ev.ID, Quote: "明天我给你发照片"}}})
	if err != nil {
		t.Fatal(err)
	}
	book, err := s.SaveCommitment(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "明天带书", Sources: []MemorySource{{EventID: ev.ID, Quote: "也把书带过去"}}})
	if err != nil || photo.ID == book.ID {
		t.Fatalf("multiple promises in one message collided: %#v %#v %v", photo, book, err)
	}
	in.TraceID, in.Text = "change-photo", "照片改到周日发。"
	correction := commitmentEvent(t, s, c, in, time.Now().UTC())
	updated, err := s.SaveCommitment(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "周日发照片", Sources: []MemorySource{{EventID: correction.ID, Quote: in.Text}}, Corrects: photo.ID, CorrectsVersion: photo.Version})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveCommitment(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "迟到的错误改期", Sources: []MemorySource{{EventID: correction.ID, Quote: in.Text}}, Corrects: photo.ID, CorrectsVersion: photo.Version})
	if !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("stale correction overwrote v%d: %v", updated.Version, err)
	}
}

func TestMaintenanceCommitmentUsesVisibleIDVersionAndRejectsLegacyBypasses(t *testing.T) {
	s, c, in := memoryFixture(t)
	ctx := context.Background()
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	base := time.Now().UTC().Add(-2 * time.Hour)
	in.TraceID, in.Text = "maintain-original", "我们周六画海边明信片。"
	original := commitmentEvent(t, s, c, in, base)
	promise, err := s.SaveCommitment(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "周六画海边明信片", Sources: []MemorySource{{EventID: original.ID, Quote: in.Text}}})
	if err != nil {
		t.Fatal(err)
	}
	in.TraceID, in.Text = "maintain-correction", "改成周日画海边明信片。"
	correction := commitmentEvent(t, s, c, in, base.Add(time.Hour))
	model := extractionFixture(func(_ context.Context, _ string, messages []map[string]string) (string, error) {
		var payload struct {
			Existing []struct {
				ID              string `json:"id"`
				Version         int    `json:"version"`
				CommitmentState string `json:"commitment_state"`
			} `json:"commitment_candidates"`
		}
		if err := json.Unmarshal([]byte(messages[0]["content"]), &payload); err != nil {
			t.Fatal(err)
		}
		seen := false
		for _, hit := range payload.Existing {
			if hit.ID == promise.ID && hit.Version == promise.Version && hit.CommitmentState == "open" {
				seen = true
			}
		}
		if !seen {
			t.Fatal("maintenance model did not receive commitment ID and revision")
		}
		out := map[string]any{"entries": []MemoryEntry{}, "commitments": []MemoryEntry{{
			Subject: in.Sender, Detail: "周日画海边明信片", Sources: []MemorySource{{EventID: correction.ID, Quote: in.Text}},
			CommitmentState: "open", Corrects: promise.ID, CorrectsVersion: promise.Version,
			// These ordinary-memory controls are intentionally ignored for a
			// commitment, rather than becoming an alternate mutation route.
			Manual: true, MergeInto: "other", Supersedes: "other",
		}}}
		b, _ := json.Marshal(out)
		return string(b), nil
	})
	engine, err := New(c, s, model, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.extractMemoryBatch(ctx, scope, []MemoryEvent{correction}); err != nil {
		t.Fatal(err)
	}
	updated, err := s.ReadMemory(ctx, []string{scope}, promise.ID)
	if err != nil || updated.Version != promise.Version+1 || len(updated.Sources) != 2 || updated.Manual || updated.MergeInto != "" || updated.Supersedes != "" {
		t.Fatalf("maintenance commitment bypassed its constrained path: %#v %v", updated, err)
	}
}
