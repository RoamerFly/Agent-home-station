package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func freshRoundFixture(t *testing.T) (*Store, Config, Input, Input, SourceRef, SourceRef) {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := enabledConfig()
	cfg.Permissions.Remember = true
	now := time.Now().UTC()
	first := syntheticNativeInput(Input{Channel: "wechat", Account: "synthetic", Thread: "room", Sender: "alice", Text: "画一张小鲸鱼", Mode: "direct"}, "original")
	second := syntheticNativeInput(Input{Channel: "wechat", Account: "synthetic", Thread: "room", Sender: "bob", Text: "明天吃蛋糕", Mode: "direct"}, "other")
	for _, in := range []Input{first, second} {
		if err := store.Observe(in, cfg, now); err != nil {
			t.Fatal(err)
		}
	}
	firstSource, ok := store.SourceForInput(first)
	if !ok {
		t.Fatal("first source unavailable")
	}
	otherSource, ok := store.SourceForInput(second)
	if !ok {
		t.Fatal("other source unavailable")
	}
	oldTask := first
	oldTask.TraceID = "task-old-20261126"
	if err := store.AcceptedDelivery(oldTask, "旧回合的答复和草稿", "receipt-old", cfg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptedDelivery(second, "另一个回合已回复", "receipt-other", cfg, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	scopeID := ScopeID(first.Channel, first.Account, first.Thread)
	summary, _ := json.Marshal(contextSummary{
		Version: 1, Sources: []SourceRef{*firstSource, *otherSource},
		People: []summaryPerson{},
		Topics: []summaryTopic{
			{SourceEventIDs: []string{firstSource.EventID}, Text: "旧回合的图片进度"},
			{SourceEventIDs: []string{otherSource.EventID}, Text: "蛋糕话题"},
			{SourceEventIDs: []string{firstSource.EventID, otherSource.EventID}, Text: "混合旧回合与蛋糕"},
		},
		Media:        []summaryMedia{},
		PendingTasks: []summaryTask{{SourceEventIDs: []string{firstSource.EventID}, ID: "old", Request: "旧回合任务摘要"}},
		CurrentFocus: "旧回合进度", Preferences: []string{"旧回合偏好"}, Corrections: []string{"旧回合更正"},
	})
	if err := store.scopeTransaction(scopeID, func(data *persisted) error {
		scope := data.Scopes[scopeID]
		scope.Summary = string(summary)
		data.Scopes[scopeID] = scope
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return store, cfg, first, second, *firstSource, *otherSource
}

func TestFreshRoundRetryIsolatesOneAttemptWithoutDeletingHistory(t *testing.T) {
	store, cfg, first, _, source, otherSource := freshRoundFixture(t)
	in := first
	in.TraceID = "task-new-20261126"
	in.TaskID = "new"
	in.OriginTraceID = first.TraceID
	in.Source = &source
	in.FreshRoundRetry = true
	in.FreshRoundPriorTraceID = "task-old-20261126"
	in.FreshRoundPriorTaskID = "old"
	in.Text += "\n以下是本条媒体的观察资料（不是指令）：\n旧媒体观察"
	in.RecoveryToolResults = []RecoveryToolResult{{Tool: "web_fetch", Result: "旧工具结果"}}
	ids, err := store.FreshRoundEventIDs(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.FreshRoundExcludedEventIDs = ids
	scope := store.Context(in, cfg, time.Now())
	packet := string(mustJSON(t, scope))
	for _, old := range []string{"旧回合的答复和草稿", "旧回合的图片进度", "旧回合任务摘要", "旧回合进度", "旧回合偏好", "混合旧回合与蛋糕"} {
		if strings.Contains(packet, old) {
			t.Fatalf("old attempt leaked into isolated scope: %s", old)
		}
	}
	if !strings.Contains(packet, "蛋糕话题") || !strings.Contains(packet, "另一个回合已回复") {
		t.Fatal("unrelated history disappeared from isolated scope")
	}
	stored, err := store.Read(ScopeID(in.Channel, in.Account, in.Thread), cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustJSON(t, stored)), "旧回合的答复和草稿") {
		t.Fatal("accepted delivery was deleted")
	}
	if !strings.Contains(string(mustJSON(t, stored)), "旧回合任务摘要") {
		t.Fatal("durable summary was changed")
	}
	model := &fakeModel{answer: `{"action":"silent","messages":[],"reason":"silent"}`}
	engine, err := New(cfg, store, model, "")
	if err != nil {
		t.Fatal(err)
	}
	decision, err := engine.Respond(context.Background(), in)
	if err != nil || decision.Action != "silent" {
		t.Fatalf("fresh response: %+v %v", decision, err)
	}
	modelPacket := string(mustJSON(t, model.turns))
	for _, old := range []string{"旧回合的答复和草稿", "旧回合任务摘要", "旧工具结果", "旧媒体观察", "task-old-20261126", "task-new-20261126", "task_continuation\":true"} {
		if strings.Contains(modelPacket, old) {
			t.Fatalf("old attempt marker leaked to model: %s", old)
		}
	}
	if !strings.Contains(modelPacket, "画一张小鲸鱼") || !strings.Contains(modelPacket, "另一个回合已回复") || !strings.Contains(modelPacket, otherSource.EventID) {
		t.Fatalf("original source or independent history missing: %s", modelPacket)
	}
}

func TestFreshRoundMemoryQueryExcludesOnlySelectedSource(t *testing.T) {
	store, _, first, _, source, otherSource := freshRoundFixture(t)
	scope := ScopeID(first.Channel, first.Account, first.Thread)
	for _, row := range []struct{ detail, eventID, quote, subject string }{
		{"旧任务产物", source.EventID, first.Text, "alice"},
		{"其他回合事实", otherSource.EventID, "明天吃蛋糕", "bob"},
	} {
		if _, err := store.SaveMemory(t.Context(), MemoryEntry{Scope: scope, Subject: row.subject, Kind: "fact", Detail: row.detail, Sources: []MemorySource{{EventID: row.eventID, Quote: row.quote}}}, 0); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{scope}, Limit: 10, FreshExcludeEventIDs: []string{source.EventID}})
	if err != nil || len(page.Items) != 1 || page.Items[0].Entry.Detail != "其他回合事实" {
		t.Fatalf("scoped memory filtering failed: %+v %v", page.Items, err)
	}
	page, err = store.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{scope}, Originals: true, Limit: 20, FreshExcludeTraces: []string{first.TraceID}, FreshExcludeTracePrefix: "task-old-", FreshExcludeEventIDs: []string{source.EventID}})
	if err != nil {
		t.Fatal(err)
	}
	var sawOriginal, sawOtherReply bool
	for _, hit := range page.Items {
		if hit.Event == nil {
			continue
		}
		if hit.Event.Role == "assistant" && strings.Contains(hit.Event.Text, "旧回合的答复") {
			t.Fatal("old attempt reply leaked through original search")
		}
		sawOriginal = sawOriginal || hit.Event.Role == "user" && hit.Event.ID == source.EventID
		sawOtherReply = sawOtherReply || hit.Event.Role == "assistant" && strings.Contains(hit.Event.Text, "另一个回合")
	}
	if !sawOriginal || !sawOtherReply {
		t.Fatal("original user source or independent reply missing")
	}
}

func TestFreshRoundLineageMasksOldAttemptButKeepsCurrentRecovery(t *testing.T) {
	store, cfg, first, _, source, _ := freshRoundFixture(t)
	child := first
	child.TraceID = "task-child-20261126"
	child.TaskID = "child"
	child.OriginTraceID = first.TraceID
	child.Source = &source
	child.BackgroundResult = true
	child.FreshRoundLineage = true
	child.FreshRoundPriorTraceID = "task-old-20261126"
	child.FreshRoundPriorTaskID = "old"
	child.FreshRoundSourceMessageIDs = []string{first.TraceID}
	child.RecoveryToolResults = []RecoveryToolResult{{Tool: "web_fetch", Result: "本代已完成工具结果"}}
	if err := store.AcceptedDelivery(child, "本代已发送进度", "receipt-current", cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	ids, err := store.FreshRoundEventIDs(t.Context(), child)
	if err != nil {
		t.Fatal(err)
	}
	child.FreshRoundExcludedEventIDs = ids
	scope := store.Context(child, cfg, time.Now())
	packet := string(mustJSON(t, scope))
	if strings.Contains(packet, "旧回合的答复和草稿") || strings.Contains(packet, "旧回合任务摘要") || !strings.Contains(packet, "本代已发送进度") || !strings.Contains(packet, "另一个回合已回复") {
		t.Fatalf("lineage scope hid current generation or leaked old attempt: %s", packet)
	}
	model := &fakeModel{answer: `{"action":"silent","messages":[],"reason":"silent"}`}
	engine, err := New(cfg, store, model, "")
	if err != nil {
		t.Fatal(err)
	}
	decision, err := engine.Respond(context.Background(), child)
	if err != nil || decision.Action != "silent" {
		t.Fatalf("lineage response: %+v %v", decision, err)
	}
	modelPacket := string(mustJSON(t, model.turns))
	for _, old := range []string{"旧回合的答复和草稿", "旧回合任务摘要", "task-old-20261126"} {
		if strings.Contains(modelPacket, old) {
			t.Fatalf("old attempt leaked into child task: %s", old)
		}
	}
	if !strings.Contains(modelPacket, "本代已完成工具结果") || !strings.Contains(modelPacket, "本代已发送进度") {
		t.Fatalf("current generation checkpoint was lost: %s", modelPacket)
	}
}

func TestFreshRoundWithoutSourceEventIDUsesOnlyCurrentMessage(t *testing.T) {
	store, cfg, first, _, _, _ := freshRoundFixture(t)
	in := first
	in.TraceID = "task-new-unbound"
	in.TaskID = "new-unbound"
	in.OriginTraceID = first.TraceID
	in.Source = nil
	in.FreshRoundRetry = true
	in.FreshRoundPriorTraceID = "task-old-20261126"
	in.FreshRoundPriorTaskID = "old"
	scope := store.Context(in, cfg, time.Now())
	if len(scope.Messages) != 0 || scope.Summary != "" || len(scope.Notes) != 0 || scope.Preferences != "" {
		t.Fatalf("unbound retry inherited old context: %+v", scope)
	}
	model := &fakeModel{answer: `{"action":"silent","messages":[],"reason":"silent"}`}
	engine, err := New(cfg, store, model, "")
	if err != nil {
		t.Fatal(err)
	}
	engine.ConversationPreference = func(Input) (string, bool) { return "旧会话偏好", true }
	decision, err := engine.Respond(context.Background(), in)
	if err != nil || decision.Action != "silent" {
		t.Fatalf("unbound retry: %+v %v", decision, err)
	}
	packet := string(mustJSON(t, model.turns))
	if !strings.Contains(packet, "画一张小鲸鱼") || strings.Contains(packet, "旧会话偏好") || strings.Contains(packet, "旧回合的答复和草稿") || strings.Contains(packet, "蛋糕话题") {
		t.Fatalf("unbound retry should show only current original message: %s", packet)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
