package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProactiveTurnPersistsOnlyExplicitObservedReflectionAndShowsItNextTime(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := enabledConfig()
	cfg.Permissions.Remember, cfg.Permissions.Proactive = true, true
	cfg.ProactiveThreads = []string{"family@chatroom"}
	now := time.Now().UTC()
	words := "分享了一段已经完整观察的海边纪录片。"
	source := SourceRef{InstanceID: "reflection-turn-fixture", EventID: "event-observed-video", MessageID: "message-observed-video", ServerID: "server-observed-video", Account: "account", Thread: "family@chatroom", Sender: "member", MessageType: 49, OccurredAt: now.Format(time.RFC3339Nano), BodyHash: sourceHash(words), Revision: 1, Integrity: "verified"}
	// Native incoming media can have no registered AssetID yet.  One observed
	// item still has a stable event-scoped identity and must remain eligible.
	incoming := Input{Channel: "wechat", Account: "account", Thread: "family@chatroom", Sender: "member", IsGroup: true, Mode: "direct", TraceID: source.MessageID, Text: words, OriginalText: &words, Source: &source, IncomingMedia: []IncomingMediaRef{{Source: source, Kind: "video", UploaderID: "member", Observation: "镜头记录海边礁石和潮汐，讲解潮间带生物。", ObservationExcerpt: "海边礁石与潮汐，讲解潮间带生物。", ObservationState: "observed", ObservationVersion: 2, ObservedRange: "0–96 秒完整音轨与采样画面"}}}
	if err = store.Observe(incoming, cfg, now); err != nil {
		t.Fatal(err)
	}
	scope := ScopeID(incoming.Channel, incoming.Account, incoming.Thread)
	candidates, err := store.ListObservedExperienceReflectionCandidates(t.Context(), scope, 3)
	if err != nil || len(candidates) != 1 || candidates[0].SourceAssetID == "" || !strings.HasPrefix(candidates[0].SourceAssetID, "event:") {
		t.Fatalf("missing event-bound candidate: %#v, %v", candidates, err)
	}
	selection, _ := json.Marshal(ExperienceReflectionSelection{Choice: 1, Note: "潮间带的画面很想以后再看看。", WantsToRevisit: true})
	var decoded Decision
	if err := json.Unmarshal([]byte(`{"action":"silent","messages":[],"reason":"nothing_to_add","experience_reflection":`+string(selection)+`}`), &decoded); err != nil || decoded.ExperienceReflection == nil || decoded.ExperienceReflection.Choice != 1 {
		t.Fatalf("decision reflection decode=%#v err=%v", decoded, err)
	}
	model := &fakeModel{answer: `{"action":"silent","messages":[],"reason":"nothing_to_add","experience_reflection":` + string(selection) + `}`}
	engine, err := New(cfg, store, model, "reflection test")
	if err != nil {
		t.Fatal(err)
	}
	proactive := Input{Channel: "wechat", Account: "account", Thread: "family@chatroom", IsGroup: true, Mode: "proactive", TraceID: "proactive-reflection-one", Text: "一次获准的自主评估"}
	decision, respondErr := engine.Respond(t.Context(), proactive)
	if respondErr != nil {
		t.Fatal(respondErr)
	}
	page, err := store.ListExperienceReflections(t.Context(), scope, ExperienceReflectionQuery{Limit: 3})
	if err != nil || len(page.Items) != 1 || page.Items[0].Note != "潮间带的画面很想以后再看看。" || page.Items[0].Source == nil || page.Items[0].Source.EventID != candidates[0].SourceEventID || page.Items[0].Source.AssetID != candidates[0].SourceAssetID {
		t.Fatalf("decision=%#v reflections=%#v err=%v", decision, page, err)
	}
	if !turnsContain(model.turns, `"experience_reflection_candidates"`) || !turnsContain(model.turns, `"choice":1`) || turnsContain(model.turns, `"source_observation_hash"`) || turnsContain(model.turns, `"experience_reflections"`) {
		t.Fatalf("first proactive prompt candidates/reflections=%#v", model.turns)
	}

	updated, err := store.UpdateExperienceReflection(t.Context(), scope, page.Items[0].ID, page.Items[0].Revision, "我想再细看潮汐变化。")
	if err != nil {
		t.Fatal(err)
	}
	model.answer = `{"action":"silent","messages":[],"reason":"nothing_to_add"}`
	proactive.TraceID = "proactive-reflection-two"
	if _, err = engine.Respond(t.Context(), proactive); err != nil {
		t.Fatal(err)
	}
	if !turnsContain(model.turns, `"experience_reflections"`) || !turnsContain(model.turns, "我想再细看潮汐变化。") || turnsContain(model.turns, "潮间带的画面很想以后再看看。") {
		t.Fatalf("next proactive prompt does not contain the current own reflection: %#v", model.turns)
	}
	if model.calls != 2 {
		t.Fatalf("proactive reflection added model calls: got %d, want 2", model.calls)
	}

	// A normal reply may carry forged-looking optional JSON, but it receives no
	// candidates and must not create a new reflection or expose the private list.
	model.answer = `{"action":"silent","messages":[],"experience_reflection":{"choice":1,"note":"普通回合不能写入。","wants_to_revisit":true}}`
	if _, err = engine.Respond(t.Context(), Input{Channel: "wechat", Account: "account", Thread: "family@chatroom", IsGroup: true, Mode: "direct", TraceID: "direct-reflection", Text: "普通消息"}); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListExperienceReflections(t.Context(), scope, ExperienceReflectionQuery{Limit: 3})
	if err != nil || len(page.Items) != 1 || turnsContain(model.turns, "experience_reflection_candidates") || turnsContain(model.turns, "experience_reflections") {
		t.Fatalf("ordinary turn persisted or exposed reflections=%#v err=%v turns=%#v", page, err, model.turns)
	}

	if _, err = store.ForgetExperienceReflection(t.Context(), scope, updated.ID, updated.Revision); err != nil {
		t.Fatal(err)
	}
	model.answer = `{"action":"silent","messages":[],"reason":"nothing_to_add"}`
	proactive.TraceID = "proactive-reflection-forgotten"
	if _, err = engine.Respond(t.Context(), proactive); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListExperienceReflections(t.Context(), scope, ExperienceReflectionQuery{Limit: 3})
	if err != nil || len(page.Items) != 0 || turnsContain(model.turns, "experience_reflection_candidates") || turnsContain(model.turns, "experience_reflections") {
		t.Fatalf("forgotten reflection was reoffered=%#v err=%v turns=%#v", page, err, model.turns)
	}
}

func TestProactiveReflectionRejectsTitleOnlyAndPreview(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := enabledConfig()
	cfg.Permissions.Remember, cfg.Permissions.Proactive = true, true
	words := "分享卡片标题：潮汐纪录片"
	source := SourceRef{InstanceID: "reflection-title-fixture", EventID: "event-title-only", MessageID: "message-title-only", Account: "account", Thread: "family@chatroom", Sender: "member", MessageType: 49, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), BodyHash: sourceHash(words), Revision: 1, Integrity: "verified"}
	incoming := Input{Channel: "wechat", Account: "account", Thread: "family@chatroom", Sender: "member", IsGroup: true, Mode: "direct", TraceID: source.MessageID, Text: words, OriginalText: &words, Source: &source, IncomingMedia: []IncomingMediaRef{{Source: source, AssetID: "asset-title-only", Kind: "share", UploaderID: "member", Observation: "标题：潮汐纪录片", ObservationState: "observed", ObservationVersion: 1}}}
	if err = store.Observe(incoming, cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	scope := ScopeID(incoming.Channel, incoming.Account, incoming.Thread)
	if candidates, listErr := store.ListObservedExperienceReflectionCandidates(t.Context(), scope, 3); listErr != nil || len(candidates) != 0 {
		t.Fatalf("title-only candidate=%#v err=%v", candidates, listErr)
	}
	model := &fakeModel{answer: `{"action":"silent","messages":[],"reason":"nothing_to_add","experience_reflection":{"choice":1,"note":"想再看","wants_to_revisit":true}}`}
	engine, err := New(cfg, store, model, "reflection test")
	if err != nil {
		t.Fatal(err)
	}
	preview := Input{Channel: "wechat", Account: "account", Thread: "family@chatroom", Mode: "proactive", Preview: true, TraceID: "preview-reflection", Text: "预演"}
	if _, err = engine.Respond(context.Background(), preview); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListExperienceReflections(t.Context(), scope, ExperienceReflectionQuery{Limit: 3})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("preview/title-only wrote reflection=%#v err=%v", page, err)
	}
	if turnsContain(model.turns, "experience_reflection_candidates") || turnsContain(model.turns, "experience_reflections") {
		t.Fatalf("preview leaked persistent reflection context: %#v", model.turns)
	}
}

func TestProactiveFeelingIsSourcedTransientAndCorrectable(t *testing.T) {
	store, _, incoming, scope, _ := observedReflectionFixture(t)
	cfg := enabledConfig()
	cfg.Permissions.Remember, cfg.Permissions.Proactive = true, true
	model := &fakeModel{answer: `{"action":"silent","messages":[],"experience_reflection":{"choice":1,"feeling":"看完觉得很温暖。"}}`}
	engine, err := New(cfg, store, model, "feeling test")
	if err != nil {
		t.Fatal(err)
	}
	proactive := Input{Channel: incoming.Channel, Account: incoming.Account, Thread: incoming.Thread, IsGroup: true, Mode: "proactive", TraceID: "feeling-one", Text: "一次获准的自主评估"}
	if _, err = engine.Respond(t.Context(), proactive); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListExperienceReflections(t.Context(), scope, ExperienceReflectionQuery{Limit: 3})
	if err != nil || len(page.Items) != 1 || page.Items[0].WantsToRevisit || page.Items[0].Note != "" || page.Items[0].Feeling != "看完觉得很温暖。" {
		t.Fatalf("feeling-only reflection=%#v err=%v", page, err)
	}

	model.answer = `{"action":"silent","messages":[]}`
	proactive.TraceID = "feeling-two"
	if _, err = engine.Respond(t.Context(), proactive); err != nil {
		t.Fatal(err)
	}
	if !turnsContain(model.turns, `"experience_mood"`) || !turnsContain(model.turns, "看完觉得很温暖。") || turnsContain(model.turns, `"experience_reflections"`) {
		t.Fatalf("fresh mood was not isolated in next proactive input: %#v", model.turns)
	}

	updated, err := store.SetExperienceReflectionContent(t.Context(), scope, page.Items[0].ID, page.Items[0].Revision, "", "现在心情很平静。")
	if err != nil {
		t.Fatal(err)
	}
	proactive.TraceID = "feeling-three"
	if _, err = engine.Respond(t.Context(), proactive); err != nil {
		t.Fatal(err)
	}
	if !turnsContain(model.turns, "现在心情很平静。") || turnsContain(model.turns, "看完觉得很温暖。") {
		t.Fatalf("corrected mood was not reflected in next proactive input: %#v", model.turns)
	}

	fading, err := store.RecentExperienceMood(t.Context(), scope, updated.Created.Add(7*time.Hour))
	if err != nil || fading == nil || fading.Feeling != "现在心情很平静。" || fading.Influence != "fading" {
		t.Fatalf("fading mood=%#v err=%v", fading, err)
	}
	expired, err := store.RecentExperienceMood(t.Context(), scope, updated.Created.Add(25*time.Hour))
	if err != nil || expired != nil {
		t.Fatalf("expired mood=%#v err=%v", expired, err)
	}
	ordinary := Input{Channel: incoming.Channel, Account: incoming.Account, Thread: incoming.Thread, IsGroup: true, Sender: incoming.Sender, Mode: "direct", TraceID: "ordinary-chat", Text: "晚上好"}
	if _, err := engine.Respond(t.Context(), ordinary); err != nil {
		t.Fatal(err)
	}
	if turnsContain(model.turns, "experience_mood") || turnsContain(model.turns, updated.Feeling) || model.calls != 4 {
		t.Fatalf("ordinary chat received a mood or triggered extra calls: calls=%d", model.calls)
	}
}

func turnsContain(turns []map[string]string, want string) bool {
	for _, turn := range turns {
		if strings.Contains(turn["content"], want) {
			return true
		}
	}
	return false
}
