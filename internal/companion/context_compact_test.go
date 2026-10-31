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
