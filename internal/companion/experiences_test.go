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
