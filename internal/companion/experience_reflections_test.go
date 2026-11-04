package companion

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func observedReflectionFixture(t *testing.T) (*Store, Config, Input, string, MemoryEvent) {
	t.Helper()
	store, cfg, in := memoryFixture(t)
	in = syntheticNativeInput(in, "reflection-share")
	in.Text = "分享了一段视频。"
	in.IsGroup = true
	in.Thread = "reflection-room@chatroom"
	in.Source.Thread = in.Thread
	in.IncomingMedia = []IncomingMediaRef{{
		Source:             *in.Source,
		AssetID:            "reflection-video",
		Kind:               "video",
		UploaderID:         in.Sender,
		Observation:        "视频讲述作者下班后给母亲回电话的经历。",
		ObservationExcerpt: "作者下班后给母亲回电话的经历。",
		ObservationState:   "verified",
		ObservationVersion: 3,
		ObservedRange:      "video 48.855s; sampled frames=6; transcript 0-48.855s (complete)",
	}}
	if err := store.Observe(in, cfg, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	var eventID string
	if err := store.db.QueryRow("SELECT id FROM events WHERE scope=? AND trace=?", scope, in.TraceID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	event, err := store.ReadEvent(t.Context(), []string{scope}, eventID)
	if err != nil {
		t.Fatal(err)
	}
	return store, cfg, in, scope, event
}

func TestObservedReflectionCandidatesUseIndexedSelection(t *testing.T) {
	store, cfg, in, scope, _ := observedReflectionFixture(t)
	// Three newer observations were explicitly decided and then forgotten.
	// They must be filtered in SQL, so the older fresh observation still wins.
	for n := 0; n < 3; n++ {
		newer := syntheticNativeInput(Input{Channel: in.Channel, Account: in.Account, Thread: in.Thread, Sender: in.Sender, Text: "新的分享"}, "reflection-decided-"+string(rune('a'+n)))
		newer.IsGroup = true
		asset := "reflection-decided-asset-" + string(rune('a'+n))
		newer.IncomingMedia = []IncomingMediaRef{{Source: *newer.Source, AssetID: asset, Kind: "video", UploaderID: in.Sender, Observation: "完整的实际观察。", ObservationExcerpt: "完整的实际观察。", ObservationState: "verified", ObservationVersion: 1, ObservedRange: "video 12s; transcript complete"}}
		if err := store.Observe(newer, cfg, time.Now().UTC().Add(time.Duration(n+1)*time.Second)); err != nil {
			t.Fatal(err)
		}
		candidates, err := store.ListObservedExperienceReflectionCandidates(t.Context(), scope, 12)
		if err != nil {
			t.Fatal(err)
		}
		var selected ObservedExperienceReflectionCandidate
		for _, candidate := range candidates {
			if candidate.SourceAssetID == asset {
				selected = candidate
				break
			}
		}
		if selected.SourceAssetID == "" {
			t.Fatalf("new source not selectable: %#v", candidates)
		}
		saved, err := store.SaveExperienceReflection(t.Context(), ExperienceReflectionInput{Scope: scope, SourceEventID: selected.SourceEventID, SourceEventRevision: selected.SourceEventRevision, SourceAssetID: selected.SourceAssetID, SourceObservationVersion: selected.SourceObservationVersion, SourceObservationHash: selected.SourceObservationHash, Note: "我想再看。", WantsToRevisit: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.ForgetExperienceReflection(t.Context(), scope, saved.ID, saved.Revision); err != nil {
			t.Fatal(err)
		}
	}
	// The partial events index keeps text-only traffic out of proactive
	// candidate selection; the bounded decode sees only typed observations.
	for n := 0; n < 40; n++ {
		text := syntheticNativeInput(Input{Channel: in.Channel, Account: in.Account, Thread: in.Thread, Sender: in.Sender, Text: "普通聊天"}, "reflection-text-"+string(rune('a'+n)))
		if err := store.Observe(text, cfg, time.Now().UTC().Add(time.Duration(n+1)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.db.Query("EXPLAIN QUERY PLAN "+reflectionCandidatesSQL, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := []string{}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "events_reflection_observable_v2") {
		t.Fatalf("candidate query lost indexed observation selection: %s", strings.Join(plan, "\n"))
	}
	if candidates, err := store.ListObservedExperienceReflectionCandidates(t.Context(), scope, 1); err != nil || len(candidates) != 1 || candidates[0].SourceAssetID != "reflection-video" {
		t.Fatalf("indexed selection=%#v err=%v", candidates, err)
	}
}

func TestExperienceReflectionRequiresExplicitVerifiedObservation(t *testing.T) {
	store, _, _, scope, event := observedReflectionFixture(t)
	candidates, err := store.ListObservedExperienceReflectionCandidates(t.Context(), scope, 5)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%#v err=%v", candidates, err)
	}
	candidate := candidates[0]
	if candidate.SourceEventID != event.ID || candidate.SourceEventRevision != event.Revision || candidate.SourceAssetID != "reflection-video" || candidate.SourceObservationVersion != 3 || candidate.SourceObservationHash == "" {
		t.Fatalf("candidate detached from observation: %#v event=%#v", candidate, event)
	}
	if _, err = store.SaveExperienceReflection(t.Context(), ExperienceReflectionInput{Scope: scope, SourceEventID: candidate.SourceEventID, SourceEventRevision: candidate.SourceEventRevision, SourceAssetID: candidate.SourceAssetID, SourceObservationVersion: candidate.SourceObservationVersion, SourceObservationHash: candidate.SourceObservationHash, Note: "我想以后再看看这种亲子沟通的话题。"}); !errors.Is(err, ErrInvalidExperienceReflection) {
		t.Fatalf("implicit model desire accepted: %v", err)
	}
	if _, err = store.SaveExperienceReflection(t.Context(), ExperienceReflectionInput{Scope: scope, SourceEventID: candidate.SourceEventID, SourceEventRevision: candidate.SourceEventRevision, SourceAssetID: candidate.SourceAssetID, SourceObservationVersion: candidate.SourceObservationVersion, SourceObservationHash: "stale", Note: "我想以后再看看这种亲子沟通的话题。", WantsToRevisit: true}); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("stale observation accepted: %v", err)
	}
	reflection, err := store.SaveExperienceReflection(t.Context(), ExperienceReflectionInput{Scope: scope, SourceEventID: candidate.SourceEventID, SourceEventRevision: candidate.SourceEventRevision, SourceAssetID: candidate.SourceAssetID, SourceObservationVersion: candidate.SourceObservationVersion, SourceObservationHash: candidate.SourceObservationHash, Note: "我想以后再看看这种亲子沟通的话题。", WantsToRevisit: true})
	if err != nil || reflection.Revision != 1 || reflection.Source == nil || reflection.Source.ObservationExcerpt == "" {
		t.Fatalf("reflection=%#v err=%v", reflection, err)
	}
	page, err := store.ListExperienceReflections(t.Context(), scope, ExperienceReflectionQuery{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != reflection.ID {
		t.Fatalf("reflection readback=%#v err=%v", page, err)
	}
	updated, err := store.UpdateExperienceReflection(t.Context(), scope, reflection.ID, reflection.Revision, "我想再找同类沟通故事看看。")
	if err != nil || updated.Revision != 2 || updated.Note != "我想再找同类沟通故事看看。" {
		t.Fatalf("update=%#v err=%v", updated, err)
	}
	forgotten, err := store.ForgetExperienceReflection(t.Context(), scope, reflection.ID, updated.Revision)
	if err != nil || forgotten.Status != "forgotten" || forgotten.Revision != 3 {
		t.Fatalf("forget=%#v err=%v", forgotten, err)
	}
	page, err = store.ListExperienceReflections(t.Context(), scope, ExperienceReflectionQuery{Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("forgotten reflection still visible=%#v err=%v", page, err)
	}
}

func TestExperienceReflectionCandidateRejectsShareEnvelopeWithoutTypedExcerpt(t *testing.T) {
	ev := MemoryEvent{ID: "source", Role: "user", Revision: 1}
	media := IncomingMediaRef{Source: SourceRef{EventID: "source", Revision: 1}, AssetID: "share", Kind: "share", Observation: "[分享来源资料] 标题与发布者文案，不是实际内容。", ObservationState: "verified", ObservationVersion: 1, ObservedRange: "share card"}
	if _, ok := observedExperienceReflectionCandidateWithIdentity(ev, media, media.AssetID); ok {
		t.Fatal("raw share-card envelope was promoted without trusted observation excerpt")
	}
	media.ObservationExcerpt = "实际已读到的内容摘要。"
	if candidate, ok := observedExperienceReflectionCandidateWithIdentity(ev, media, media.AssetID); !ok || candidate.Summary != media.ObservationExcerpt {
		t.Fatalf("typed excerpt not accepted safely: %#v ok=%v", candidate, ok)
	}
}

func TestExperienceReflectionTypedPartsExcludePublisherDescription(t *testing.T) {
	ev := MemoryEvent{ID: "source", Role: "user", Revision: 1}
	media := IncomingMediaRef{Source: SourceRef{EventID: "source", Revision: 1}, AssetID: "share", Kind: "share", Observation: "发布者标题和简介\n实际观察", ObservationState: "observed", ObservationVersion: 1, ObservedRange: "aggregate", ObservationExcerpt: "旧兼容摘要", ObservationParts: []ObservationPart{{Kind: ObservationPublisherDescription, Range: "share card", Excerpt: "发布者说这是亲子视频"}}}
	if _, ok := observedExperienceReflectionCandidateWithIdentity(ev, media, media.AssetID); ok {
		t.Fatal("publisher description was promoted as a lived experience")
	}
	media.ObservationParts = append(media.ObservationParts, ObservationPart{Kind: ObservationVisualFrames, Range: "frames 0-5", Excerpt: "画面里有人在海边奔跑。"})
	candidate, ok := observedExperienceReflectionCandidateWithIdentity(ev, media, media.AssetID)
	if !ok || candidate.Summary != "画面里有人在海边奔跑。" || candidate.ObservedRange != "frames 0-5" {
		t.Fatalf("actual visual observation not selected safely: %#v ok=%v", candidate, ok)
	}
}

func TestExperienceReflectionHidesWhenRootObservationChanges(t *testing.T) {
	store, _, _, scope, event := observedReflectionFixture(t)
	candidates, err := store.ListObservedExperienceReflectionCandidates(t.Context(), scope, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatal(candidates, err)
	}
	candidate := candidates[0]
	reflection, err := store.SaveExperienceReflection(t.Context(), ExperienceReflectionInput{Scope: scope, SourceEventID: candidate.SourceEventID, SourceEventRevision: candidate.SourceEventRevision, SourceAssetID: candidate.SourceAssetID, SourceObservationVersion: candidate.SourceObservationVersion, SourceObservationHash: candidate.SourceObservationHash, Note: "我想再看这种内容。", WantsToRevisit: true})
	if err != nil {
		t.Fatal(err)
	}
	// An observation revision is a new interpretation of the same source, so a
	// reflection rooted in the old observation must disappear rather than be
	// silently rebound to the new content.
	if event.Source == nil {
		t.Fatal("stored source missing")
	}
	ref := *event.Source
	if err = store.CacheIncomingObservation("wechat", ref, "修订后的实际视频观察。"); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListExperienceReflections(t.Context(), scope, ExperienceReflectionQuery{Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("stale reflection visible=%#v err=%v", page, err)
	}
	if _, err = store.UpdateExperienceReflection(t.Context(), scope, reflection.ID, reflection.Revision, "不能更新旧观察反思"); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("old observation was editable: %v", err)
	}
	stillStored, err := store.readExperienceReflection(t.Context(), scope, reflection.ID)
	if err != nil || stillStored.Revision != reflection.Revision || stillStored.Note != reflection.Note {
		t.Fatalf("stale update changed persisted reflection=%#v err=%v", stillStored, err)
	}
	// A model response produced before an admin forget/observation correction
	// must not save its old candidate afterwards.
	if _, err = store.ForgetExperienceReflection(t.Context(), scope, reflection.ID, reflection.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveExperienceReflection(t.Context(), ExperienceReflectionInput{Scope: scope, SourceEventID: candidate.SourceEventID, SourceEventRevision: candidate.SourceEventRevision, SourceAssetID: candidate.SourceAssetID, SourceObservationVersion: candidate.SourceObservationVersion, SourceObservationHash: candidate.SourceObservationHash, Note: "陈旧模型输出不能重建反思。", WantsToRevisit: true}); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("stale post-forget model save accepted: %v", err)
	}
}

func TestExperienceReflectionDoesNotResurrectForgottenRootOrSkipOlderVisiblePage(t *testing.T) {
	store, cfg, in, scope, _ := observedReflectionFixture(t)
	firstCandidates, err := store.ListObservedExperienceReflectionCandidates(t.Context(), scope, 5)
	if err != nil || len(firstCandidates) != 1 {
		t.Fatal(firstCandidates, err)
	}
	first := firstCandidates[0]
	older, err := store.SaveExperienceReflection(t.Context(), ExperienceReflectionInput{Scope: scope, SourceEventID: first.SourceEventID, SourceEventRevision: first.SourceEventRevision, SourceAssetID: first.SourceAssetID, SourceObservationVersion: first.SourceObservationVersion, SourceObservationHash: first.SourceObservationHash, Feeling: "温暖", Note: "我想再看这种沟通故事。", WantsToRevisit: true})
	if err != nil {
		t.Fatal(err)
	}
	// Different wording is an idempotent re-proposal of the same evidence, not
	// a second reflection that could bypass a later correction or forgetting.
	replayed, err := store.SaveExperienceReflection(t.Context(), ExperienceReflectionInput{Scope: scope, SourceEventID: first.SourceEventID, SourceEventRevision: first.SourceEventRevision, SourceAssetID: first.SourceAssetID, SourceObservationVersion: first.SourceObservationVersion, SourceObservationHash: first.SourceObservationHash, Note: "换一种说法也想再看。", WantsToRevisit: true})
	if err != nil || replayed.ID != older.ID || replayed.Note != older.Note {
		t.Fatalf("reproposal=%#v older=%#v err=%v", replayed, older, err)
	}

	newerInput := syntheticNativeInput(Input{Channel: in.Channel, Account: in.Account, Thread: in.Thread, Sender: in.Sender, Text: "又分享了一段视频。"}, "reflection-newer")
	newerInput.IsGroup = true
	newerInput.IncomingMedia = []IncomingMediaRef{{Source: *newerInput.Source, AssetID: "reflection-newer-video", Kind: "video", UploaderID: in.Sender, Observation: "一段已完整实际读取的新视频。", ObservationExcerpt: "已完整实际读取的新视频。", ObservationState: "verified", ObservationVersion: 1, ObservedRange: "video 20s; transcript 0-20s (complete)"}}
	if err = store.Observe(newerInput, cfg, time.Now().UTC().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.ListObservedExperienceReflectionCandidates(t.Context(), scope, 5)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%#v err=%v", candidates, err)
	}
	newer := candidates[0]
	if _, err = store.SaveExperienceReflection(t.Context(), ExperienceReflectionInput{Scope: scope, SourceEventID: newer.SourceEventID, SourceEventRevision: newer.SourceEventRevision, SourceAssetID: newer.SourceAssetID, SourceObservationVersion: newer.SourceObservationVersion, SourceObservationHash: newer.SourceObservationHash, Feeling: "好奇", Note: "我想再看这个。", WantsToRevisit: true}); err != nil {
		t.Fatal(err)
	}
	mood, err := store.RecentExperienceMood(t.Context(), scope, time.Now())
	if err != nil || mood == nil || mood.Feeling != "好奇" {
		t.Fatalf("latest mood=%v err=%v", mood, err)
	}
	// Simulate original-event revocation. The newest persisted reflection is
	// stale, but pagination must keep scanning and still return the older one.
	if _, err = store.db.Exec("UPDATE events SET suppressed=1 WHERE id=?", newer.SourceEventID); err != nil {
		t.Fatal(err)
	}
	mood, err = store.RecentExperienceMood(t.Context(), scope, time.Now())
	if err != nil || mood == nil || mood.Feeling != "温暖" {
		t.Fatalf("withdrawn observation still influenced the fish: %v, %v", mood, err)
	}
	page, err := store.ListExperienceReflections(t.Context(), scope, ExperienceReflectionQuery{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != older.ID {
		t.Fatalf("page skipped older verified reflection=%#v err=%v", page, err)
	}
	if _, err = store.ForgetExperienceReflection(t.Context(), scope, older.ID, older.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveExperienceReflection(t.Context(), ExperienceReflectionInput{Scope: scope, SourceEventID: first.SourceEventID, SourceEventRevision: first.SourceEventRevision, SourceAssetID: first.SourceAssetID, SourceObservationVersion: first.SourceObservationVersion, SourceObservationHash: first.SourceObservationHash, Note: "忘记后不能复活。", WantsToRevisit: true}); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("forgotten root resurrected: %v", err)
	}
}

func TestExperienceReflectionFeelingMoodSurvivesRestartAndCanBeCorrectedOrForgotten(t *testing.T) {
	store, _, _, scope, _ := observedReflectionFixture(t)
	candidates, err := store.ListObservedExperienceReflectionCandidates(t.Context(), scope, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%#v err=%v", candidates, err)
	}
	candidate := candidates[0]
	reflection, err := store.SaveExperienceReflection(t.Context(), ExperienceReflectionInput{
		Scope: scope, SourceEventID: candidate.SourceEventID, SourceEventRevision: candidate.SourceEventRevision,
		SourceAssetID: candidate.SourceAssetID, SourceObservationVersion: candidate.SourceObservationVersion,
		SourceObservationHash: candidate.SourceObservationHash, Feeling: "有点温暖", WantsToRevisit: false,
	})
	if err != nil || reflection.Note != "" || reflection.WantsToRevisit || reflection.Feeling != "有点温暖" {
		t.Fatalf("feeling-only reflection=%#v err=%v", reflection, err)
	}
	// One verified observation yields one durable reflection. A feeling is not
	// an invitation for the model to rediscover and repeatedly rewrite it.
	if candidates, err = store.ListObservedExperienceReflectionCandidates(t.Context(), scope, 1); err != nil || len(candidates) != 0 {
		t.Fatalf("feeling-only reflection re-entered candidate list=%#v err=%v", candidates, err)
	}
	interestPage, err := store.ListExperienceReflections(t.Context(), scope, ExperienceReflectionQuery{Limit: 1, InterestOnly: true})
	if err != nil || len(interestPage.Items) != 0 {
		t.Fatalf("feeling-only reflection leaked into interest list=%#v err=%v", interestPage, err)
	}
	mood, err := store.RecentExperienceMood(t.Context(), scope, reflection.Created.Add(5*time.Hour))
	if err != nil || mood == nil || mood.Feeling != "有点温暖" || mood.Influence != "fresh" {
		t.Fatalf("fresh mood=%#v err=%v", mood, err)
	}

	// The new content setter can change a feeling without fabricating a second
	// experience. The legacy note editor remains an interest-only edit and
	// therefore keeps the feeling intact.
	corrected, err := store.SetExperienceReflectionContent(t.Context(), scope, reflection.ID, reflection.Revision, "", "有点期待")
	if err != nil || corrected.Revision != reflection.Revision+1 || corrected.Created != reflection.Created || corrected.Feeling != "有点期待" || corrected.Note != "" || corrected.WantsToRevisit {
		t.Fatalf("feeling correction=%#v err=%v", corrected, err)
	}
	interest, err := store.UpdateExperienceReflection(t.Context(), scope, corrected.ID, corrected.Revision, "也想再看同类内容")
	if err != nil || !interest.WantsToRevisit || interest.Note == "" || interest.Feeling != "有点期待" {
		t.Fatalf("legacy interest update lost feeling=%#v err=%v", interest, err)
	}
	if _, err = store.SetExperienceReflectionContent(t.Context(), scope, interest.ID, interest.Revision, "", ""); !errors.Is(err, ErrInvalidExperienceReflection) {
		t.Fatalf("empty content accepted instead of requiring forget: %v", err)
	}

	// Make the original experience seven hours old in both the indexed column
	// and its source payload, then reopen. Correcting content did not refresh
	// Created, so restart must retain the fading—not fresh—classification.
	aged := interest
	aged.Created = time.Now().UTC().Add(-7 * time.Hour)
	payload, err := json.Marshal(aged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec("UPDATE experience_reflections SET created=?,payload=? WHERE id=?", memoryTime(aged.Created), payload, aged.ID); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(store.path)
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	mood, err = reopened.RecentExperienceMood(t.Context(), scope, aged.Created.Add(7*time.Hour))
	if err != nil || mood == nil || mood.Feeling != "有点期待" || mood.Influence != "fading" {
		t.Fatalf("reopened fading mood=%#v err=%v", mood, err)
	}
	forgotten, err := reopened.ForgetExperienceReflection(t.Context(), scope, interest.ID, interest.Revision)
	if err != nil || forgotten.Status != "forgotten" {
		t.Fatalf("forget mood reflection=%#v err=%v", forgotten, err)
	}
	mood, err = reopened.RecentExperienceMood(t.Context(), scope, aged.Created.Add(8*time.Hour))
	if err != nil || mood != nil {
		t.Fatalf("forgotten mood remained current=%#v err=%v", mood, err)
	}
}
