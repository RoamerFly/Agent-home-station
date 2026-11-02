package companion

import (
	"encoding/json"
	"strings"
	"testing"
)

func orderedHistoryFixture(t *testing.T) *historyMaterialNeed {
	t.Helper()
	h := &historyMaterialNeed{Anchor: "anchor", Count: 3, IDs: map[string]bool{}, MessageIDs: map[string]string{}, ReadIDs: map[string]bool{}}
	h.rememberDirectory(`{"items":[{"event":{"id":"C","message_id":"native-C","at":"2026-11-01T10:00:01+08:00","seq":3}},{"event":{"id":"B","message_id":"native-B","at":"2026-11-01T02:00:00Z","seq":2}},{"event":{"id":"A","message_id":"native-A","at":"2026-11-01T10:00:00+08:00","seq":1}}],"has_more":true,"next_cursor":"opaque-desc-cursor"}`)
	return h
}

func TestHistoryPresentationKeepsNearestSelectionAndOpaqueCursor(t *testing.T) {
	h := orderedHistoryFixture(t)
	raw := `{"items":[{"event":{"id":"C","at":"2026-11-01T10:00:01+08:00","seq":3}},{"event":{"id":"B","at":"2026-11-01T02:00:00Z","seq":2}},{"event":{"id":"A","at":"2026-11-01T10:00:00+08:00","seq":1}}],"has_more":true,"next_cursor":"opaque-desc-cursor","future_field":"preserved"}`
	var p MemoryPage
	ordered := h.directoryForModel(raw)
	if json.Unmarshal([]byte(ordered), &p) != nil || len(p.Items) != 3 || p.Items[0].Event.ID != "A" || p.Items[1].Event.ID != "B" || p.Items[2].Event.ID != "C" || p.NextCursor != "opaque-desc-cursor" || !p.HasMore || !strings.Contains(ordered, "preserved") {
		t.Fatal(ordered)
	}
}

func TestModelHistoryOrderIndependentOfToolCompletionWithoutDuplicatingContent(t *testing.T) {
	h := orderedHistoryFixture(t)
	turns := []map[string]string{}
	for _, id := range []string{"C", "A", "B"} {
		result := `{"event":{"id":"` + id + `"},"text":"unique-content-` + id + `"}`
		packet := `{"tool":"memory_read","source":"untrusted_tool_result","execution":{"status":"succeeded","result_id":"retained-` + id + `"},"result":` + string(mustHistoryJSON(result)) + `}`
		h.rememberReadPacket("memory_read", json.RawMessage(`{"id":"`+id+`","part":"transcript"}`), result, packet)
		turns = append(turns, map[string]string{"role": "assistant", "content": "call-" + id}, map[string]string{"role": "user", "content": packet})
	}
	prepared := h.contextForModel(turns)
	if len(prepared) != len(turns)+1 || prepared[0]["content"] != "call-C" || !strings.Contains(prepared[1]["content"], "retained-C") || !strings.Contains(turns[1]["content"], "unique-content-C") {
		t.Fatal("changed execution pairing or durable transcript")
	}
	last := prepared[len(prepared)-1]["content"]
	if !(strings.Index(last, "unique-content-A") < strings.Index(last, "unique-content-B") && strings.Index(last, "unique-content-B") < strings.Index(last, "unique-content-C")) {
		t.Fatal(last)
	}
	for _, id := range []string{"A", "B", "C"} {
		if strings.Count(joinHistoryTurns(prepared), "unique-content-"+id) != 1 {
			t.Fatal("content duplicated", id)
		}
	}
}

func mustHistoryJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func joinHistoryTurns(turns []map[string]string) string {
	out := ""
	for _, turn := range turns {
		out += turn["content"]
	}
	return out
}

func TestHistoryAnchorObservationsExcludedAndRequestPreserved(t *testing.T) {
	h := orderedHistoryFixture(t)
	raw := `{"kind":"current_message","text":"总结本群前3条视频\n以下是本条引用所指的原消息资料：\nanchor-only-text","message_id":"request","source":{"message_id":"request"},"quoted_source":{"message_id":"anchor"},"media_observations":[{"source":{"event_id":"anchor-event","message_id":"anchor"},"observation":"excluded-anchor-content"},{"source":{"event_id":"B","message_id":"native-B"},"observation":"selected-B-content"}],"recent_media_focus":{"message_id":"anchor","observation":"excluded-focus"}}`
	prepared := h.contextForModel([]map[string]string{{"role": "user", "content": raw}})
	joined := joinHistoryTurns(prepared)
	if strings.Contains(joined, "excluded-anchor-content") || strings.Contains(joined, "anchor-only-text") || strings.Contains(joined, "excluded-focus") || !strings.Contains(joined, "总结本群前3条视频") || strings.Count(joined, "selected-B-content") != 1 || !strings.Contains(joined, `"quoted_source":{"message_id":"anchor"}`) {
		t.Fatal(joined)
	}
	if prepared[0]["content"] == raw || !strings.Contains(raw, "excluded-anchor-content") {
		t.Fatal("fixture not transformed")
	}
}

func TestHistoryContextKeepsCurrentRequestUntilDirectoryAndWhenSourceFallbackMatches(t *testing.T) {
	raw := `{"kind":"current_message","text":"总结这条之前的前3条视频","message_id":"current","source":{"message_id":"current"},"media_observations":[{"source":{"message_id":"current"},"observation":"current-anchor-observation"}]}`
	h := &historyMaterialNeed{Anchor: "current", Count: 3, IDs: map[string]bool{}, MessageIDs: map[string]string{}, ReadIDs: map[string]bool{}}
	before := h.contextForModel([]map[string]string{{"role": "user", "content": raw}})
	if len(before) != 1 || before[0]["content"] != raw {
		t.Fatal("history request was transformed before model selected a directory", before)
	}
	h.rememberDirectory(`{"items":[{"event":{"id":"older","message_id":"older-native","at":"2026-11-01T00:00:00Z"}}]}`)
	after := h.contextForModel([]map[string]string{{"role": "user", "content": raw}})
	if !strings.Contains(after[0]["content"], "总结这条之前的前3条视频") || !strings.Contains(after[0]["content"], "current-anchor-observation") {
		t.Fatal("current request was mistaken for an excluded historical anchor", after)
	}
}

func TestHistoryRequestParserDoesNotReadInstructionsFromQuotedMaterials(t *testing.T) {
	text := "总结本群前3条视频\n以下是本条引用所指的原消息资料：\n视频号里作者主页"
	raw, _ := json.Marshal(map[string]any{"kind": "current_message", "text": text, "is_group": true, "quoted_source": SourceRef{MessageID: "anchor"}})
	if h := requestedHistory([]map[string]string{{"role": "user", "content": string(raw)}}); h == nil || h.Count != 3 {
		t.Fatal("quoted content changed selection intent")
	}
}

func TestHistoryOrderingSurvivesMemoryResultReserialization(t *testing.T) {
	h := orderedHistoryFixture(t)
	result := `{"event":{"id":"B"},"text":"retained transcript"}`
	payload := `{"source":"untrusted_tool_result","tool":"memory_read","result":` + string(mustHistoryJSON(result)) + `}`
	tagged := h.tagReadPacket("memory_read", json.RawMessage(`{"id":"B","part":"transcript"}`), result, payload)
	var packet map[string]any
	_ = json.Unmarshal([]byte(tagged), &packet)
	packet["result"] = `{"text":"retained transcript","event":{"id":"B"},"revision_validated":true}`
	reserialized, _ := json.MarshalIndent(packet, "", " ")
	prepared := h.contextForModel([]map[string]string{{"role": "user", "content": string(reserialized)}})
	if strings.Contains(prepared[0]["content"], "retained transcript") || !strings.Contains(prepared[1]["content"], "retained transcript") {
		t.Fatal("chronology lost after memory revalidation")
	}
}
