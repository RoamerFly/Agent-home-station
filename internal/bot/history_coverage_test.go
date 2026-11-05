package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

type coverageHistoryModel struct{ step int }

func (*coverageHistoryModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", nil
}

func (m *coverageHistoryModel) ChatFunctions(_ context.Context, _ string, _ []map[string]string, _ []mcptools.Tool) (string, error) {
	m.step++
	if m.step == 1 {
		return `{"action":"tool","tool":"memory_search","arguments":{"originals":true,"before_message_id":"history-coverage","media_kind":"video_or_share","order":"time_desc","metadata_only":true,"limit":8}}`, nil
	}
	if m.step <= 9 {
		return fmt.Sprintf(`{"action":"tool","tool":"memory_read","arguments":{"id":"E%d","event":true,"part":"transcript"}}`, m.step-1), nil
	}
	return `{"action":"reply","messages":["六条已整理，另外两条读取失败。"]}`, nil
}

type coverageHistoryTools struct{}

func (coverageHistoryTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "memory_search", InputSchema: json.RawMessage(`{"type":"object"}`)}, {Name: "memory_read", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}

func (coverageHistoryTools) Call(_ context.Context, name string, args json.RawMessage) (string, error) {
	if name == "memory_search" {
		return `{"items":[{"event":{"id":"E8","message_id":"M8","at":"2026-11-01T08:00:00Z"}},{"event":{"id":"E7","message_id":"M7","at":"2026-11-01T07:00:00Z"}},{"event":{"id":"E6","message_id":"M6","at":"2026-11-01T06:00:00Z"}},{"event":{"id":"E5","message_id":"M5","at":"2026-11-01T05:00:00Z"}},{"event":{"id":"E4","message_id":"M4","at":"2026-11-01T04:00:00Z"}},{"event":{"id":"E3","message_id":"M3","at":"2026-11-01T03:00:00Z"}},{"event":{"id":"E2","message_id":"M2","at":"2026-11-01T02:00:00Z"}},{"event":{"id":"E1","message_id":"M1","at":"2026-11-01T01:00:00Z"}}],"has_more":false}`, nil
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if in.ID == "E7" || in.ID == "E8" {
		return "", errors.New("synthetic stored transcript unavailable")
	}
	return fmt.Sprintf(`{"event":{"id":"%s"},"text":"已读取 %s","has_more":false}`, in.ID, in.ID), nil
}

func TestPartialHistoryCoverageIsRecordedAlongsideAcceptedReply(t *testing.T) {
	cfg := companion.Defaults()
	cfg.Enabled = true
	cfg.Permissions.Remember = true
	cfg.ReplyMinMS, cfg.ReplyMaxMS = 0, 0
	cfg.QuietStart, cfg.QuietEnd = "00:00", "00:00"
	cfg.Conversation = &companion.ConversationSettings{Enabled: false, WakeMinSeconds: 30, WakeMaxSeconds: 600}
	store, err := companion.NewStore(t.TempDir())
	if store != nil {
		t.Cleanup(func() { _ = store.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	engine, err := companion.New(cfg, store, &coverageHistoryModel{}, "role")
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools, engine.MaxToolCalls = coverageHistoryTools{}, 10
	records := []Record{}
	sender := &fakeSender{}
	w, err := New(Config{StateDir: t.TempDir(), AllowedThreads: []string{"filehelper"}, Record: func(r Record) { records = append(records, r) }}, &fakeSource{}, engine, sender)
	if err != nil {
		t.Fatal(err)
	}
	// Run through the same observed-source/evidence binding used by a normal
	// incoming request.  The coverage record must coexist with an accepted
	// delivery, rather than rely on a legacy draft that production correctly
	// refuses to send from a private conversation.
	in := observeSyntheticMediaRequest(t, w, engine, companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "person", TraceID: "history-coverage", Mode: "direct", Text: "总结这条之前的前面8条视频"})
	if in.ReplyContext == nil {
		t.Fatal("synthetic incoming request was not evidence-bound")
	}
	decision, err := engine.Respond(context.Background(), in)
	if err != nil || decision.Action != "reply" || decision.HistoryCoverage == nil || decision.HistoryCoverage.Read != 6 || decision.HistoryCoverage.Failed != 2 {
		t.Fatal("normal tool loop did not retain actual partial coverage", err, decision, decision.HistoryCoverage)
	}
	w.finishCharacter(context.Background(), engine, in, decision, nil, nil, false, false)
	if sender.calls != 1 {
		t.Fatal("reply was not submitted to simulated WeChat sender", sender.calls, records)
	}
	var coverageRecord *Record
	accepted := false
	for i := range records {
		if records[i].Event == "history_read_coverage" {
			coverageRecord = &records[i]
		}
		accepted = accepted || records[i].Event == "message_sent"
	}
	if coverageRecord == nil || coverageRecord.Status != "partial" || coverageRecord.Reason != "history_read_partial" || !accepted {
		t.Fatal("partial coverage or accepted receipt missing", records)
	}
	var persisted companion.HistoryReadCoverage
	if json.Unmarshal([]byte(coverageRecord.DecisionNote), &persisted) != nil || persisted.Read != 6 || persisted.Failed != 2 || persisted.Attempted != 8 {
		t.Fatal("durable coverage detail lost", coverageRecord.DecisionNote, persisted)
	}
}
