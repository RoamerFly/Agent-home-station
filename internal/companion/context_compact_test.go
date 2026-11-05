package companion

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestHistoryBudgetSkipsOversizedObservationWithoutLosingEarlierSource(t *testing.T) {
	cfg := enabledConfig()
	cfg.Context = &ContextSettings{RecentMessages: 20, MaxInputChars: 4000}
	e := &Engine{Config: cfg, model: &contextModel{}}
	scope := Scope{ID: "synthetic", Messages: []Turn{
		{Role: "user", Sender: "a", TraceID: "small", Text: "我的父亲明天回来", Time: time.Unix(10, 0)},
		{Role: "user", Sender: "a", TraceID: "large", Text: "[视频]", FullObservation: strings.Repeat("合成观察", 4000), Time: time.Unix(20, 0)},
	}}
	turns := e.contextTurns(scope, Input{Sender: "a", TraceID: "current"})
	if len(turns) != 1 || !strings.Contains(turns[0]["content"], "我的父亲明天回来") {
		t.Fatal("large observation evicted earlier source", turns)
	}
	var payload struct {
		Omitted int `json:"omitted_messages"`
	}
	if json.Unmarshal([]byte(turns[0]["content"]), &payload) != nil || payload.Omitted != 1 {
		t.Fatal("omission not reported")
	}
}

func TestCompactHistoryPreservesUnknownActorsAndVersion(t *testing.T) {
	e := &Engine{Config: enabledConfig()}
	row := e.turnEvidenceFields("synthetic", Turn{Role: "user", Sender: "a", TraceID: "m", Text: "你好", Time: time.Unix(10, 0)}, 512)
	before, _ := json.Marshal(row)
	compactHistoryRow(row)
	after, _ := json.Marshal(row)
	if len(after) >= len(before) {
		t.Fatal("no reduction")
	}
	if row["message_sender_id"] != "a" || row["speaker_id"] != "a" || row["text"] != "你好" || row["evidence_kind"] != "user_source" {
		t.Fatal(row)
	}
	unknown := map[string]any{"voice_speaker_id": nil, "source": &SourceRef{Revision: 3}, "scope": "s"}
	compactHistoryRow(unknown)
	if _, ok := unknown["voice_speaker_id"]; !ok {
		t.Fatal("unknown identity removed")
	}
	if unknown["source"].(*SourceRef).Revision != 3 {
		t.Fatal("source version lost")
	}
	t.Logf("synthetic row bytes %d -> %d, estimated tokens %d -> %d", len(before), len(after), EstimateTokens(string(before)), EstimateTokens(string(after)))
}

func TestTimelineKeepsQuotedRequestAndActualAttachmentAcrossRecentWindow(t *testing.T) {
	cfg := enabledConfig()
	cfg.Context = &ContextSettings{RecentMessages: 5, MaxInputChars: 24000}
	e := &Engine{Config: cfg, model: &contextModel{}}
	base := time.Unix(100, 0)
	quoted := Turn{
		Role: "user", Sender: "alice", SenderName: "Alice", TraceID: "shared-local-id", EventID: "event-alice", Text: "cloth 和 fabric 怎么读？",
		Source: &SourceRef{EventID: "event-alice", MessageID: "shared-local-id", Sender: "alice", Account: "a", Thread: "room", Integrity: "verified"}, Time: base,
	}
	// A historic import reused the local ID for another participant. The quote
	// must resolve by stable EventID, not whichever record was most recently
	// visited.
	other := Turn{Role: "user", Sender: "bob", TraceID: "shared-local-id", EventID: "event-bob", Text: "unrelated", Time: base.Add(time.Second)}
	attachment := Turn{Role: "assistant", Sender: "bot", TraceID: "transport-media", RequestTraceID: "shared-local-id", Text: "[媒体附件] fabric 发音", Time: base.Add(2 * time.Second), Media: &MediaMemory{
		Key: "voice-1", Kind: "voice", Title: "fabric 发音", AssetID: "asset-voice", State: "accepted", AcceptedParts: 1, TotalParts: 1,
		RequestTraceID: "shared-local-id", RequesterID: "alice", SynthesisTextKnown: true, SynthesisText: "fabric /ˈfæbrɪk/",
	}}
	captionA := Turn{Role: "assistant", Sender: "bot", TraceID: "local-a", DeliveryID: "receipt-a", RequestTraceID: "shared-local-id", Text: "已补上。", Time: base.Add(3 * time.Second)}
	captionB := Turn{Role: "assistant", Sender: "bot", TraceID: "local-b", DeliveryID: "receipt-b", RequestTraceID: "shared-local-id", Text: "已补上。", Time: base.Add(4 * time.Second)}
	noise := []Turn{
		{Role: "user", Sender: "bob", TraceID: "n1", Text: "群聊背景 1", Time: base.Add(5 * time.Second)},
		{Role: "user", Sender: "bob", TraceID: "n2", Text: "群聊背景 2", Time: base.Add(6 * time.Second)},
		{Role: "user", Sender: "alice", TraceID: "follow", Text: "还有一个呢", QuotedSource: &SourceRef{EventID: "event-alice", MessageID: "shared-local-id", Sender: "alice"}, Time: base.Add(7 * time.Second)},
	}
	scope := Scope{ID: "synthetic", Messages: append([]Turn{quoted, attachment, captionA, captionB, other}, noise...)}
	input := Input{Sender: "alice", TraceID: "current", IsGroup: true, QuotedSource: &SourceRef{EventID: "event-alice", MessageID: "shared-local-id", Sender: "alice"}}
	turns := e.contextTurns(scope, input)
	if len(turns) != 1 {
		t.Fatal(turns)
	}
	var packet struct {
		Timeline []map[string]any `json:"timeline"`
		Omitted  int              `json:"omitted_messages"`
	}
	if err := json.Unmarshal([]byte(turns[0]["content"]), &packet); err != nil {
		t.Fatal(err)
	}
	if packet.Omitted < 1 { // selected evidence exceeds the normal five-row window
		t.Fatal("normal-window omissions not reported", packet.Omitted)
	}
	var attachmentRow map[string]any
	captionReceipts := map[string]bool{}
	order := []string{}
	for _, row := range packet.Timeline {
		if id, _ := row["message_id"].(string); id != "" {
			order = append(order, id)
		}
		if row["timeline_event"] == "assistant_attachment" {
			attachmentRow = row
		}
		if id, _ := row["delivery_id"].(string); id != "" {
			captionReceipts[id] = true
		}
	}
	if attachmentRow == nil || attachmentRow["text"] != nil {
		t.Fatal("attachment transport label leaked as a spoken bubble", attachmentRow)
	}
	delivery, _ := attachmentRow["attachment_delivery"].(map[string]any)
	if delivery["requester_id"] != "alice" || delivery["receipt_state"] != "accepted" || delivery["synthesis_text"] != "fabric /ˈfæbrɪk/" || delivery["synthesis_text_state"] != "verified" {
		t.Fatal("attachment truth missing", delivery)
	}
	if !captionReceipts["receipt-a"] || !captionReceipts["receipt-b"] {
		t.Fatal("same-text actual bubbles were deduplicated", packet.Timeline)
	}
	if indexOf(order, "shared-local-id") < 0 || indexOf(order, "transport-media") < indexOf(order, "shared-local-id") {
		t.Fatal("timeline is not chronological", order)
	}
}

func TestTimelinePresentationKeepsFileAndImageAsGenericEvidence(t *testing.T) {
	e := &Engine{Config: enabledConfig()}
	now := time.Unix(200, 0)
	image := Turn{Role: "user", Sender: "alice", TraceID: "image", Text: "", IncomingMedia: []IncomingMediaRef{{Kind: "image", UploaderID: "alice", Observation: "一张合成图片", ObservationState: "available"}}, Time: now}
	file := Turn{Role: "assistant", Sender: "bot", TraceID: "file-send", RequestTraceID: "image", Text: "[媒体附件] 说明文件", Time: now.Add(time.Second), Media: &MediaMemory{Key: "file", Kind: "file", Title: "说明文件", State: "accepted", RequestTraceID: "image", RequesterID: "alice"}}
	scope := Scope{ID: "synthetic", Messages: []Turn{image, file}}
	turns := e.contextTurns(scope, Input{Sender: "alice", TraceID: "now", IsGroup: true})
	if len(turns) != 2 || !strings.Contains(turns[1]["content"], `"attachment_kind":"file"`) || !strings.Contains(turns[0]["content"], `"kind":"image"`) {
		t.Fatal("generic media evidence missing", turns)
	}
}

func TestTimelineReservesRelatedReceiptBeforeLongRecentNoise(t *testing.T) {
	cfg := enabledConfig()
	cfg.Context = &ContextSettings{RecentMessages: 2, MaxInputChars: 2200}
	e := &Engine{Config: cfg, model: &contextModel{}}
	now := time.Unix(300, 0)
	request := Turn{Role: "user", Sender: "alice", TraceID: "request", EventID: "request-event", Text: "把两个单词都读一遍", Time: now}
	receipt := Turn{Role: "assistant", TraceID: "voice", Text: "[媒体附件] 读音", Time: now.Add(time.Second), Media: &MediaMemory{Key: "voice", Kind: "voice", Title: "读音", State: "accepted", RequestTraceID: "request", RequesterID: "alice", SynthesisTextKnown: true, SynthesisText: "cloth; fabric"}}
	noise := []Turn{
		{Role: "user", Sender: "bob", TraceID: "noise-1", Text: strings.Repeat("无关背景", 500), Time: now.Add(2 * time.Second)},
		{Role: "user", Sender: "bob", TraceID: "noise-2", Text: strings.Repeat("无关背景", 500), Time: now.Add(3 * time.Second)},
		{Role: "user", Sender: "bob", TraceID: "noise-3", Text: strings.Repeat("无关背景", 500), Time: now.Add(4 * time.Second)},
	}
	turns := e.contextTurns(Scope{ID: "synthetic", Messages: append([]Turn{request, receipt}, noise...)}, Input{Sender: "alice", TraceID: "follow", Text: "还有一个呢", IsGroup: true})
	if len(turns) != 1 || !strings.Contains(turns[0]["content"], `"synthesis_text":"cloth; fabric"`) || !strings.Contains(turns[0]["content"], "把两个单词都读一遍") {
		t.Fatal("related request/receipt lost to newer noise", turns)
	}
	if strings.Contains(turns[0]["content"], "noise-3") {
		t.Fatal("unrelated newest noise consumed reserved evidence budget", turns)
	}
}

func indexOf(items []string, want string) int {
	for i, item := range items {
		if item == want {
			return i
		}
	}
	return -1
}
