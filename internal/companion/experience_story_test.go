package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestSharedExperienceStoryUsesCurrentVersionsAndSourceVisibility builds the
// first 2.3 story through normal durable event and reply assembly paths.
func TestSharedExperienceStoryUsesCurrentVersionsAndSourceVisibility(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := enabledConfig()
	cfg.Grounding = &GroundingSettings{}
	cfg.Permissions.Remember = true
	cfg.MemoryMessages = 2
	base := time.Now().UTC().Add(-48 * time.Hour)
	in := storyNativeInput("wechat", "story-account", "xiaolin", "xiaolin", "story-sat", "我们约好周六画一张海边明信片。", base)
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	first := commitmentEvent(t, store, cfg, in, base)
	firstMemory, err := store.SaveCommitment(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Kind: "episode", Detail: "小林和大肥鱼约好周六画一张海边明信片。", Summary: "周六画海边明信片", Sources: []MemorySource{{EventID: first.ID, Quote: in.Text}}, CommitmentState: "open"})
	if err != nil {
		t.Fatal(err)
	}
	in = storyNativeInput("wechat", "story-account", "xiaolin", "xiaolin", "story-sun", "改到周日，还是画海边明信片。", base.Add(24*time.Hour))
	second := commitmentEvent(t, store, cfg, in, base.Add(24*time.Hour))
	corrected, err := store.SaveCommitment(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Kind: "episode", Detail: "小林和大肥鱼约好画海边明信片，已改到周日。", Summary: "周日画海边明信片", Sources: []MemorySource{{EventID: second.ID, Quote: in.Text}}, CommitmentState: "open", Corrects: firstMemory.ID, CorrectsVersion: firstMemory.Version})
	if err != nil {
		t.Fatal(err)
	}
	if corrected.ID != firstMemory.ID || corrected.Version <= firstMemory.Version {
		t.Fatalf("correction must retain one experience identity: initial=%#v corrected=%#v", firstMemory, corrected)
	}

	assertExperience := func(want MemoryEntry) {
		t.Helper()
		items, err := storySharedExperiences(ctx, store, scope)
		if err != nil || len(items) != 1 {
			t.Fatalf("shared experiences=%#v err=%v", items, err)
		}
		got := items[0]
		if got.ID != want.ID || got.Version != want.Version || got.Detail != want.Detail || len(got.Sources) != 2 || got.Sources[0].EventID != first.ID || got.Sources[1].EventID != second.ID {
			t.Fatalf("experience projection detached from current source-backed version: %#v", got)
		}
	}
	assertExperience(corrected)

	// Move both source bubbles out of the small recent window, restart, and
	// inspect the normal chat model request. Listing and recall must still name
	// the same current durable identity and version.
	for n, text := range []string{"今天风大。", "晚饭吃什么？", "明天带伞。"} {
		later := storyNativeInput("wechat", "story-account", "xiaolin", "xiaolin", "later-"+string(rune('a'+n)), text, base.Add(25*time.Hour+time.Duration(n)*time.Minute))
		if err := store.Observe(later, cfg, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	assertExperience(corrected)

	model := &fakeModel{answer: `{"action":"reply","messages":["周日画。"]}`}
	engine, err := New(cfg, store, model, "story-policy")
	if err != nil {
		t.Fatal(err)
	}
	query := storyNativeInput("wechat", "story-account", "xiaolin", "xiaolin", "story-ask", "我们约了什么？", time.Now().UTC())
	if _, err = engine.Respond(ctx, query); err != nil {
		t.Fatal(err)
	}
	item := storyMemoryItem(t, storyLongTermMemoryPacket(t, model.turns), corrected.ID)
	if item["version"] != float64(corrected.Version) || !strings.Contains(item["text"].(string), "周日") {
		t.Fatalf("chat recall and experience list diverged: %#v", item)
	}

	// Cross-account and cross-scope callers have no experience projection.
	otherScope := ScopeID("qq", "same-name", "xiaolin")
	other, err := storySharedExperiences(ctx, store, otherScope)
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-account shared experience leaked: %#v err=%v", other, err)
	}

	// A cursor is an assertion about the current, source-verified view.  Add a
	// second valid episode solely to obtain a multi-page listing, then prove a
	// forgotten source invalidates a cursor instead of continuing from a stale
	// view that still contained the old commitment.
	extraInput := storyNativeInput("wechat", "story-account", "xiaolin", "xiaolin", "story-extra", "上周一起看过海边的照片。", base.Add(26*time.Hour))
	extraEvent := commitmentEvent(t, store, cfg, extraInput, base.Add(26*time.Hour))
	if _, err = store.SaveMemory(ctx, MemoryEntry{Scope: scope, Subject: extraInput.Sender, Kind: "episode", Detail: "一起看过海边的照片。", Sources: []MemorySource{{EventID: extraEvent.ID, Quote: extraInput.Text}}}, 0); err != nil {
		t.Fatal(err)
	}
	firstPage, err := store.ListSharedExperiences(ctx, scope, ExperienceQuery{Limit: 1})
	if err != nil || !firstPage.HasMore || firstPage.NextCursor == "" {
		t.Fatalf("expected a paged source-backed experience view: %#v err=%v", firstPage, err)
	}

	// Forgetting any cited source blocks both the list and normal recall. The
	// source-only deletion also proves this is not merely hiding a card in UI.
	blocker, err := store.SaveMemory(ctx, MemoryEntry{Scope: scope, Subject: "xiaolin", Kind: "fact", Detail: "需要忘记最初约定", Sources: []MemorySource{{EventID: first.ID, Quote: "我们约好周六画一张海边明信片。"}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	blocker.Status = "deleted"
	if _, err = store.SaveMemory(ctx, blocker, blocker.Version); err != nil {
		t.Fatal(err)
	}
	items, err := storySharedExperiences(ctx, store, scope)
	if err != nil || len(items) != 1 || items[0].ID == corrected.ID {
		t.Fatalf("forgotten source remained in experience list: %#v err=%v", items, err)
	}
	if _, err = store.ListSharedExperiences(ctx, scope, ExperienceQuery{Limit: 1, Cursor: firstPage.NextCursor}); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("forgetting must invalidate a stale experience cursor, err=%v", err)
	}
	model = &fakeModel{answer: `{"action":"reply","messages":["我看不到那段。"]}`}
	engine, err = New(cfg, store, model, "story-policy")
	if err != nil {
		t.Fatal(err)
	}
	query.TraceID, query.Text = "story-after-forget", "我们约了什么？"
	query = syntheticNativeInput(query, query.TraceID)
	if _, err = engine.Respond(ctx, query); err != nil {
		t.Fatal(err)
	}
	for _, turn := range model.turns {
		if strings.Contains(turn["content"], corrected.ID) || strings.Contains(turn["content"], "海边明信片") {
			t.Fatalf("forgotten experience leaked back into chat: %s", turn["content"])
		}
	}
}

// storySharedExperiences uses the same source-validated projection as the
// relationship workbench; stories must not query a second, stale cache.
func storySharedExperiences(ctx context.Context, store *Store, scope string) ([]SharedExperience, error) {
	page, err := store.ListSharedExperiences(ctx, scope, ExperienceQuery{Limit: 100})
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func TestSharedExperienceGroupStatementKeepsWhoActuallyExperiencedWhat(t *testing.T) {
	store, cfg, in := memoryFixture(t)
	in.Thread = "family@chatroom"
	in.TraceID = "group-wedding"
	in.Text = "今天参加了同事儿子的喜酒，盼我儿子以后也有这一天。"
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	if err := store.Observe(in, cfg, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	page, err := store.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{scope}, Originals: true, Query: "喜酒"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Event == nil {
		t.Fatalf("wedding source unavailable: %#v %v", page, err)
	}
	event := page.Items[0].Event
	entry, err := store.SaveMemory(t.Context(), MemoryEntry{Scope: scope, Subject: in.Sender, Kind: "episode", Detail: "发言者陈述：今天参加了同事儿子的喜酒，盼我儿子以后也有这一天。", Summary: "发言者陈述：今天参加了同事儿子的喜酒，盼我儿子以后也有这一天。", Sources: []MemorySource{{EventID: event.ID, Quote: in.Text}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	items, err := storySharedExperiences(t.Context(), store, scope)
	if err != nil || len(items) != 1 || items[0].ID != entry.ID || items[0].Perspective != "source_statement" || !strings.Contains(items[0].Detail, "同事儿子的喜酒") || !strings.Contains(items[0].Detail, "盼我儿子以后") || strings.Contains(items[0].Detail, "已经成家") {
		t.Fatalf("group experience changed a reported wish into a personal event: %#v err=%v", items, err)
	}
	body, _ := json.Marshal(items[0])
	if !strings.Contains(string(body), event.ID) {
		t.Fatalf("group experience has no source identity: %s", body)
	}
}
