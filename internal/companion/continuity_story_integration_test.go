package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestContinuityStorySurvivesCorrectionRestartAndScopeBoundary exercises the
// same durable event, continuity and request-assembly paths that a normal
// chat turn uses.  The capture model is only an offline transport fixture: the
// assertions inspect the request built by Engine, not a claimed model answer.
func TestContinuityStorySurvivesCorrectionRestartAndScopeBoundary(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	config := enabledConfig()
	config.Grounding = &GroundingSettings{} // This story checks normal chat assembly, not a second review request.
	config.Permissions.Remember = true
	config.MemoryMessages = 2 // Push the original exchange out of recent context.

	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-48 * time.Hour)
	initial := storyNativeInput("wechat", "main-account", "xiaolin", "xiaolin", "promise-saturday", "我们约好周六画一张海边明信片。", base)
	correction := storyNativeInput("wechat", "main-account", "xiaolin", "xiaolin", "promise-sunday", "改成周日，还是画海边。", base.Add(24*time.Hour))
	scopeID := ScopeID(initial.Channel, initial.Account, initial.Thread)
	maintenanceModel := &storyCommitmentMaintenanceModel{t: t}
	maintenanceEngine, err := New(config, store, maintenanceModel, "story-policy")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(initial, config, time.Now()); err != nil {
		t.Fatal(err)
	}
	turns := store.Context(initial, config, time.Now()).Messages
	if len(turns) != 1 || turns[0].EventID == "" {
		t.Fatalf("missing durable initial promise source: %#v", turns)
	}
	initialEvent := turns[0].EventID
	if err := maintenanceEngine.MaintainScope(ctx, scopeID, true); err != nil {
		t.Fatal(err)
	}
	promise := storyOnlyCommitment(t, store, scopeID)
	if promise.CommitmentState != "open" || !promise.Continuity || !promise.Pinned || promise.Status != "active" {
		t.Fatalf("initial commitment not saved as an open sourced episode: %#v", promise)
	}
	if err := store.Observe(correction, config, time.Now()); err != nil {
		t.Fatal(err)
	}
	turns = store.Context(correction, config, time.Now()).Messages
	if len(turns) != 2 || turns[1].EventID == "" {
		t.Fatalf("missing durable correction source: %#v", turns)
	}
	correctionEvent := turns[1].EventID
	if err := maintenanceEngine.MaintainScope(ctx, scopeID, true); err != nil {
		t.Fatal(err)
	}
	continuity := storyOnlyCommitment(t, store, scopeID)
	if continuity.ID != promise.ID || continuity.Version <= promise.Version || continuity.CommitmentState != "open" || !continuity.Continuity || !continuity.Pinned || continuity.Status != "active" || len(continuity.Sources) != 2 {
		t.Fatalf("correction did not update the same open sourced commitment: initial=%#v corrected=%#v", promise, continuity)
	}
	if maintenanceModel.calls != 2 || maintenanceModel.firstSourceID != initialEvent || maintenanceModel.secondSourceID != correctionEvent || maintenanceModel.correctedCandidateID != promise.ID || maintenanceModel.correctedCandidateVersion != promise.Version {
		t.Fatalf("incremental maintenance did not receive actual sources/candidate identity: %#v", maintenanceModel)
	}
	current, revisions, err := store.MemoryHistory(ctx, []string{scopeID}, continuity.ID)
	if err != nil || current.Version != continuity.Version || len(revisions) == 0 || !strings.Contains(revisions[0].Detail, "周六") {
		t.Fatalf("corrected commitment lost auditable original revision: current=%#v revisions=%#v err=%v", current, revisions, err)
	}

	// Simulate unrelated later traffic.  The two source bubbles must now be
	// absent from normal recent history; their evidence stays in SQLite.
	for i, text := range []string{"今天风有点大。", "晚饭吃什么？", "明天记得带伞。"} {
		in := storyNativeInput("wechat", "main-account", "xiaolin", "xiaolin", "later-"+string(rune('a'+i)), text, base.Add(25*time.Hour+time.Duration(i)*time.Minute))
		if err := store.Observe(in, config, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	recent := store.Context(correction, config, time.Now()).Messages
	for _, turn := range recent {
		if turn.EventID == initialEvent || turn.EventID == correctionEvent || strings.Contains(turn.FullText, "海边明信片") {
			t.Fatalf("old promise incorrectly remained in recent context: %#v", recent)
		}
	}
	if len(recent) != 2 {
		t.Fatalf("expected only configured recent history, got %#v", recent)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// A restart must reload the durable correction, rather than recover it from
	// an in-process cache or from the pruned conversation prefix.
	store, err = NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	model := &fakeModel{answer: `{"action":"reply","messages":["收到，我们周日画。"]}`}
	engine, err := New(config, store, model, "story-policy")
	if err != nil {
		t.Fatal(err)
	}
	query := storyNativeInput("wechat", "main-account", "xiaolin", "xiaolin", "story-query", "我们约了什么？", time.Now().UTC())
	if _, err = engine.Respond(ctx, query); err != nil {
		t.Fatal(err)
	}
	packet := storyLongTermMemoryPacket(t, model.turns)
	item := storyMemoryItem(t, packet, continuity.ID)
	if got, _ := item["text"].(string); !strings.Contains(got, "周日") {
		t.Fatalf("corrected continuity was not the current recalled text: %#v", item)
	}
	if got, _ := item["scope"].(string); got != scopeID {
		t.Fatalf("recalled continuity scope=%q want %q", got, scopeID)
	}
	quotes, ok := item["source_quotes"].([]any)
	if !ok || len(quotes) != 2 || !storySourceQuoteContains(quotes, initial.Text) || !storySourceQuoteContains(quotes, correction.Text) {
		t.Fatalf("request lost original or correction provenance: %#v", item["source_quotes"])
	}
	if !strings.Contains(storyEventText(t, store, scopeID, initialEvent), "周六") || !strings.Contains(storyEventText(t, store, scopeID, correctionEvent), "周日") {
		t.Fatal("memory sources cannot be traced back to their original messages")
	}
	for _, turn := range model.turns {
		if strings.Contains(turn["content"], initial.Text) || strings.Contains(turn["content"], correction.Text) {
			var envelope map[string]any
			if json.Unmarshal([]byte(turn["content"]), &envelope) == nil && envelope["kind"] != "long_term_memory" {
				t.Fatalf("pruned raw exchange leaked into model history instead of recalled memory: %s", turn["content"])
			}
		}
	}

	// The same display name on another account/platform cannot receive this
	// episode.  This is a normal constructed request, not a direct DB lookup.
	otherModel := &fakeModel{answer: `{"action":"reply","messages":["我先看当前会话。"]}`}
	otherEngine, err := New(config, store, otherModel, "story-policy")
	if err != nil {
		t.Fatal(err)
	}
	other := storyNativeInput("qq", "same-nickname-account", "xiaolin", "xiaolin", "same-name-query", "我们约了什么？", time.Now().UTC())
	if _, err = otherEngine.Respond(ctx, other); err != nil {
		t.Fatal(err)
	}
	for _, turn := range otherModel.turns {
		if strings.Contains(turn["content"], "海边明信片") || strings.Contains(turn["content"], initialEvent) || strings.Contains(turn["content"], correctionEvent) {
			t.Fatalf("continuity leaked to same nickname on another platform/account: %s", turn["content"])
		}
	}
}

func storyNativeInput(channel, account, thread, sender, id, text string, at time.Time) Input {
	in := syntheticNativeInput(Input{Channel: channel, Account: account, Thread: thread, Sender: sender, Mode: "direct", Text: text}, id)
	stamp := at.UTC().Format(time.RFC3339Nano)
	in.MessageTime = stamp
	in.Source.OccurredAt = stamp
	return in
}

// storyCommitmentMaintenanceModel is an offline extraction transport. It
// derives every candidate source and correction target from the real payload
// that MaintainScope assembled, so this test cannot pass by inventing IDs in
// the test body.
type storyCommitmentMaintenanceModel struct {
	t                         *testing.T
	calls                     int
	firstSourceID             string
	secondSourceID            string
	correctedCandidateID      string
	correctedCandidateVersion int
}

func (m *storyCommitmentMaintenanceModel) Chat(_ context.Context, system string, turns []map[string]string) (string, error) {
	m.t.Helper()
	if !strings.Contains(system, "有来源的会话记忆整理器") || len(turns) != 1 {
		m.t.Fatalf("unexpected non-maintenance model call: system=%q turns=%#v", system, turns)
	}
	var payload struct {
		Sources     []MemoryEvent `json:"sources"`
		Commitments []MemoryEntry `json:"commitment_candidates"`
	}
	if err := json.Unmarshal([]byte(turns[0]["content"]), &payload); err != nil {
		m.t.Fatal(err)
	}
	if len(payload.Sources) != 1 {
		m.t.Fatalf("maintenance batch should contain exactly the newly observed source: %#v", payload.Sources)
	}
	m.calls++
	ev := payload.Sources[0]
	candidate := MemoryEntry{
		Subject:         ev.Sender,
		Kind:            "episode",
		Keywords:        []string{"明信片", "海边", "约定"},
		Sources:         []MemorySource{{EventID: ev.ID, Quote: ev.Text}},
		CommitmentState: "open",
	}
	switch m.calls {
	case 1:
		m.firstSourceID = ev.ID
		candidate.Detail = "小林和大肥鱼约好周六画一张海边明信片。"
		candidate.Summary = "周六画海边明信片"
	case 2:
		m.secondSourceID = ev.ID
		if len(payload.Commitments) != 1 || payload.Commitments[0].ID == "" {
			m.t.Fatalf("correction batch did not receive the real existing commitment candidate: %#v", payload.Commitments)
		}
		m.correctedCandidateID = payload.Commitments[0].ID
		m.correctedCandidateVersion = payload.Commitments[0].Version
		candidate.Detail = "小林和大肥鱼约好画海边明信片；已从周六改为周日。"
		candidate.Summary = "周日画海边明信片"
		candidate.Keywords = append(candidate.Keywords, "周日")
		candidate.Corrects = m.correctedCandidateID
		candidate.CorrectsVersion = m.correctedCandidateVersion
	default:
		m.t.Fatalf("unexpected maintenance extraction call %d", m.calls)
	}
	response, err := json.Marshal(map[string]any{"entries": []MemoryEntry{}, "commitments": []MemoryEntry{candidate}})
	if err != nil {
		m.t.Fatal(err)
	}
	return string(response), nil
}

func storyOnlyCommitment(t *testing.T, store *Store, scope string) MemoryEntry {
	t.Helper()
	page, err := store.SearchMemory(context.Background(), MemoryQuery{Scopes: []string{scope}, Pinned: true, Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	commitments := []MemoryEntry{}
	for _, hit := range page.Items {
		if hit.Entry != nil && hit.Entry.Continuity && hit.Entry.CommitmentState != "" {
			commitments = append(commitments, *hit.Entry)
		}
	}
	if len(commitments) != 1 {
		t.Fatalf("expected exactly one commitment, got %#v", commitments)
	}
	return commitments[0]
}

func storyLongTermMemoryPacket(t *testing.T, turns []map[string]string) map[string]any {
	t.Helper()
	for _, turn := range turns {
		var packet map[string]any
		if json.Unmarshal([]byte(turn["content"]), &packet) == nil && packet["kind"] == "long_term_memory" {
			return packet
		}
	}
	t.Fatalf("actual model request does not contain a long-term-memory packet: %#v", turns)
	return nil
}

func storyMemoryItem(t *testing.T, packet map[string]any, id string) map[string]any {
	t.Helper()
	items, ok := packet["items"].([]any)
	if !ok {
		t.Fatalf("memory packet items malformed: %#v", packet)
	}
	for _, row := range items {
		item, _ := row.(map[string]any)
		if item["id"] == id {
			return item
		}
	}
	t.Fatalf("continuity %q missing from actual recall packet: %#v", id, packet)
	return nil
}

func storySourceQuoteContains(rows []any, want string) bool {
	for _, row := range rows {
		if source, ok := row.(map[string]any); ok && source["quote"] == want {
			return true
		}
	}
	return false
}

func storyEventText(t *testing.T, store *Store, scope, eventID string) string {
	t.Helper()
	event, err := store.ReadEvent(context.Background(), []string{scope}, eventID)
	if err != nil {
		t.Fatal(err)
	}
	return event.Text
}
