package companion

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestListSharedExperiencesKeepsGroupRevisionAndInvalidatesCursor(t *testing.T) {
	store, cfg, in := memoryFixture(t)
	in.Thread = "family@chatroom"
	in.TraceID = "experience-group"
	in.Text = "今天参加了同事儿子的喜酒，盼我儿子以后也有这一天。"
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	if err := store.Observe(in, cfg, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	originals, err := store.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{scope}, Originals: true, Query: "喜酒", Limit: 5})
	if err != nil || len(originals.Items) != 1 || originals.Items[0].Event == nil {
		t.Fatalf("original source=%#v err=%v", originals, err)
	}
	event := originals.Items[0].Event
	entry, err := store.SaveMemory(t.Context(), MemoryEntry{
		Scope: scope, Subject: in.Sender, Kind: "episode",
		// This is a corrected, source-backed interpretation. The list must not
		// replace it with a separately synthesized text representation.
		Detail:  "发言者参加同事儿子的喜酒，并表达希望自己的儿子将来也能成家。",
		Sources: []MemorySource{{EventID: event.ID, Quote: in.Text}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ListSharedExperiences(t.Context(), scope, ExperienceQuery{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("experiences=%#v err=%v", page, err)
	}
	got := page.Items[0]
	if got.ID != entry.ID || got.Detail != entry.Detail || got.Perspective != "source_statement" || len(got.Sources) != 1 || got.Sources[0].EventID != event.ID || got.Sources[0].Quote != in.Text {
		t.Fatalf("group experience detached current revision or source: %#v", got)
	}
	second := in
	second.TraceID = "experience-second"
	second.Text = "明天一起整理婚礼照片。"
	if err = store.Observe(second, cfg, time.Now().UTC().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	more, err := store.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{scope}, Originals: true, Query: "婚礼照片", Limit: 5})
	if err != nil || len(more.Items) != 1 || more.Items[0].Event == nil {
		t.Fatalf("second source=%#v err=%v", more, err)
	}
	if _, err = store.SaveMemory(t.Context(), MemoryEntry{Scope: scope, Subject: second.Sender, Kind: "episode", Detail: "发言者提议明天一起整理婚礼照片。", Sources: []MemorySource{{EventID: more.Items[0].Event.ID, Quote: second.Text}}}, 0); err != nil {
		t.Fatal(err)
	}
	firstPage, err := store.ListSharedExperiences(t.Context(), scope, ExperienceQuery{Limit: 1})
	if err != nil || !firstPage.HasMore || firstPage.NextCursor == "" {
		t.Fatalf("expected a real paged result before source block: %#v err=%v", firstPage, err)
	}

	// A continuation cursor is bound to the store epoch. Forgetting the cited
	// source changes it, so a stale page cannot quietly skip to a card whose
	// evidence was just removed.
	blocker, err := store.SaveMemory(t.Context(), MemoryEntry{Scope: scope, Subject: in.Sender, Kind: "fact", Detail: "需要忘记这句话", Sources: []MemorySource{{EventID: event.ID, Quote: in.Text}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	blocker.Status = "deleted"
	if _, err = store.SaveMemory(t.Context(), blocker, blocker.Version); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListSharedExperiences(t.Context(), scope, ExperienceQuery{Limit: 1})
	if err != nil || len(page.Items) != 1 || strings.Contains(page.Items[0].Detail, "喜酒") {
		t.Fatalf("blocked source should be filtered before pagination: %#v err=%v", page, err)
	}
	// A stale continuation cannot retrieve a page after one of its sources was
	// blocked, and a manually forged one cannot retrieve it either.
	_, err = store.ListSharedExperiences(t.Context(), scope, ExperienceQuery{Cursor: firstPage.NextCursor, Limit: 1})
	if !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("stale cursor error=%v", err)
	}
	if strings.Contains(got.Detail, "已经成家") {
		t.Fatalf("wish was promoted to completed event: %#v", got)
	}
}

func TestExperiencePageFiltersChangedQuotesBeforeLimiting(t *testing.T) {
	store, cfg, in := memoryFixture(t)
	ctx := t.Context()
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	save := func(trace, words string, quotes ...string) MemoryEntry {
		t.Helper()
		in.TraceID, in.Text = trace, words
		event := commitmentEvent(t, store, cfg, in, time.Now())
		var sources []MemorySource
		for _, quote := range quotes {
			sources = append(sources, MemorySource{EventID: event.ID, Quote: quote})
		}
		entry, err := store.SaveMemory(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Kind: "episode", Detail: words, Sources: sources}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return entry
	}
	valid := save("trip", "今天去了海边。", "海边")
	stale := save("walk", "先散步，再画画。", "散步", "画画")
	// Reproduce an old database with an active derived row that outlived a
	// source edit. The index contains one event, but both quotes must match.
	if _, err := store.db.ExecContext(ctx, "UPDATE events SET text=? WHERE id=?", "只散步。", stale.Sources[0].EventID); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListSharedExperiences(ctx, scope, ExperienceQuery{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != valid.ID || page.HasMore {
		t.Fatalf("invalid source displaced a visible experience: %#v, %v", page, err)
	}
}

func TestExperienceQueryRejectsUnsupportedView(t *testing.T) {
	store, _, in := memoryFixture(t)
	_, err := store.ListSharedExperiences(t.Context(), ScopeID(in.Channel, in.Account, in.Thread), ExperienceQuery{View: "all_relations"})
	if !errors.Is(err, ErrInvalidExperienceQuery) {
		t.Fatalf("unsupported view error=%v", err)
	}
}

func TestRelationExperienceViewFiltersByStableParticipantAndAssertionRevision(t *testing.T) {
	store, entry, event := relationFixture(t, true)
	ctx := t.Context()
	// Store two relations in one memory document. Filtering the father object
	// must not attach the mother's assertion merely because its prose shares a
	// subject and source scope.
	scopeState, err := store.Read(entry.Scope, Defaults(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	motherInput := Input{Channel: scopeState.Channel, Account: scopeState.Account, Thread: scopeState.Thread, Sender: "alice", Addressees: []string{"bob"}, TraceID: "mother-relation", Text: "你妈刚视频，刚挂掉"}
	cfg := Defaults()
	cfg.Permissions.Remember = true
	if err = store.Observe(motherInput, cfg, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	motherSource, err := store.SearchMemory(ctx, MemoryQuery{Scopes: []string{entry.Scope}, Originals: true, Query: motherInput.Text, Limit: 5})
	if err != nil {
		t.Fatalf("mother source=%#v err=%v", motherSource, err)
	}
	var motherEvent *MemoryEvent
	for _, hit := range motherSource.Items {
		if hit.Event != nil && hit.Event.Trace == motherInput.TraceID {
			motherEvent = hit.Event
			break
		}
	}
	if motherEvent == nil {
		t.Fatalf("mother source not found=%#v", motherSource)
	}
	entry.Sources = append(entry.Sources, MemorySource{EventID: motherEvent.ID, Quote: motherInput.Text})
	entry.Assertions = append(entry.Assertions, MemoryAssertion{Subject: "bob", Predicate: "mother", Object: "relative:bob:mother", AssertedBy: "alice", State: "reported", SubjectBasis: "addressee", SubjectText: "你", ObjectBasis: "relative", ObjectText: "妈", PredicateText: "妈", Sources: []AssertionSource{{EventID: motherEvent.ID, EventRevision: motherEvent.Revision, Quote: motherInput.Text}}})
	saved, err := store.SaveMemory(ctx, entry, 0)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ListSharedExperiences(ctx, entry.Scope, ExperienceQuery{View: "relations", Person: "relative:bob:father", Limit: 5})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != saved.ID || len(page.Items[0].Assertions) != 1 || page.Items[0].Assertions[0].Object != "relative:bob:father" {
		t.Fatalf("stable relation=%#v err=%v", page, err)
	}
	page, err = store.ListSharedExperiences(ctx, entry.Scope, ExperienceQuery{View: "relations", Person: "bob-same-nickname-other-id", Limit: 5})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("same nickname matched a different stable ID=%#v err=%v", page, err)
	}
	// The entry source quote is still present, but the assertion's source
	// revision is not: a relation may not survive that stale binding.
	if _, err = store.db.Exec("UPDATE events SET revision=revision+1 WHERE id=?", event.ID); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListSharedExperiences(ctx, entry.Scope, ExperienceQuery{View: "relations", Person: "relative:bob:father", Limit: 5})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("stale assertion survived=%#v err=%v", page, err)
	}
	page, err = store.ListSharedExperiences(ctx, entry.Scope, ExperienceQuery{View: "relations", Person: "bob"})
	if err != nil || len(page.Items) != 1 || len(page.Items[0].Assertions) != 1 || page.Items[0].Assertions[0].Object != "relative:bob:mother" ||
		len(page.Items[0].Sources) != 1 || page.Items[0].Sources[0].EventID != motherEvent.ID {
		t.Fatalf("unaffected relation lost its own source: %#v, %v", page, err)
	}
}

func TestOpenCommitmentExperienceViewExcludesCancelledAndPaginates(t *testing.T) {
	store, cfg, in := memoryFixture(t)
	ctx := t.Context()
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	base := time.Now().UTC().Add(-time.Hour)
	create := func(trace, text string, at time.Time) MemoryEvent {
		in.TraceID, in.Text = trace, text
		return commitmentEvent(t, store, cfg, in, at)
	}
	first := create("open-one", "我明天发照片。", base)
	one, err := store.SaveCommitment(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "明天发照片", Sources: []MemorySource{{EventID: first.ID, Quote: in.Text}}})
	if err != nil {
		t.Fatal(err)
	}
	second := create("open-two", "我明天发书。", base.Add(time.Minute))
	two, err := store.SaveCommitment(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "明天发书", Sources: []MemorySource{{EventID: second.ID, Quote: in.Text}}})
	if err != nil {
		t.Fatal(err)
	}
	// A newer, explicit source may move the commitment without making a new
	// card. The subsequent cancellation must target that new revision.
	reschedule := create("reschedule-one", "照片改周日发。", base.Add(90*time.Second))
	one, err = store.SaveCommitment(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "周日发照片", Sources: []MemorySource{{EventID: reschedule.ID, Quote: in.Text}}, Corrects: one.ID, CorrectsVersion: one.Version})
	if err != nil {
		t.Fatal(err)
	}
	// A matching subject in another conversation must never enter this scope's
	// relationship/commitment workbench.
	other := in
	other.Thread = "other-family@chatroom"
	other.TraceID, other.Text = "other-scope", "我明天发另一份照片。"
	otherEvent := commitmentEvent(t, store, cfg, other, base.Add(2*time.Minute))
	if _, err = store.SaveCommitment(ctx, MemoryEntry{Scope: ScopeID(other.Channel, other.Account, other.Thread), Subject: other.Sender, Detail: "另一份照片", Sources: []MemorySource{{EventID: otherEvent.ID, Quote: other.Text}}}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListSharedExperiences(ctx, scope, ExperienceQuery{View: "open_commitments", Person: in.Sender, Limit: 1})
	if err != nil || len(page.Items) != 1 || !page.HasMore || page.NextCursor == "" || page.Items[0].CommitmentState != "open" {
		t.Fatalf("open commitment first page=%#v err=%v", page, err)
	}
	more, err := store.ListSharedExperiences(ctx, scope, ExperienceQuery{View: "open_commitments", Person: in.Sender, Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(more.Items) != 1 || more.Items[0].ID == page.Items[0].ID {
		t.Fatalf("open commitment continuation=%#v err=%v", more, err)
	}
	allOpen, err := store.ListSharedExperiences(ctx, scope, ExperienceQuery{View: "open_commitments", Person: in.Sender, Limit: 5})
	if err != nil || len(allOpen.Items) != 2 {
		t.Fatalf("scope isolation open commitments=%#v err=%v", allOpen, err)
	}
	for _, item := range allOpen.Items {
		if item.ID == one.ID && item.Detail != "周日发照片" {
			t.Fatalf("rescheduled commitment not visible=%#v", item)
		}
	}
	cancel := create("cancel-one", "照片不发了，取消。", base.Add(3*time.Minute))
	if _, err = store.SaveCommitment(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "照片已取消", Sources: []MemorySource{{EventID: cancel.ID, Quote: in.Text}}, CommitmentState: "cancelled", Corrects: one.ID, CorrectsVersion: one.Version}); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListSharedExperiences(ctx, scope, ExperienceQuery{View: "open_commitments", Person: in.Sender, Limit: 5})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != two.ID {
		t.Fatalf("cancelled commitment leaked into open view=%#v err=%v", page, err)
	}
}
