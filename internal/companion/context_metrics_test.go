package companion

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/mcptools"
)

func TestContextMetricsKeepsTotalSeparateFromOverlappingDetails(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tools := []mcptools.Tool{{Name: "private-tool-marker", Description: "private-description-marker", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	catalog, _ := json.Marshal(tools)
	turn := `{"kind":"conversation_context","summary":{"text":"private-summary-marker"},"timeline":[{"quoted_message":{"text":"private-quote-marker"}}]}`
	if err := store.recordContextMetrics(context.Background(), "policy", []map[string]string{{"role": "user", "content": turn}}, catalog); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ContextMetrics(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("metrics unavailable: rows=%d err=%v", len(rows), err)
	}
	row := rows[0]
	want := EstimateTokens("policy") + EstimateTokens(string(catalog)) + EstimateTokens(turn) + 8
	components := row["components"].(map[string]int)
	total := 0
	for _, tokens := range components {
		total += tokens
	}
	details := row["detail_bytes"].(map[string]int)
	encoded, _ := json.Marshal(row)
	if row["estimated_input_tokens"] != want || total != want || details["history_summary"] == 0 || details["history_quotes"] == 0 || strings.Contains(string(encoded), "private-") {
		t.Fatalf("metric total or privacy regression: total=%d want=%d details=%v", total, want, details)
	}
	if err := store.recordContextMetrics(context.Background(), "policy", []map[string]string{{"role": "user", "content": turn}}, nil); err != nil {
		t.Fatal("non-native catalog metric failed", err)
	}
}

func TestContextMetricsInterleavedRunsAndProviderRetries(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctxA := context.WithValue(t.Context(), contextMetricRunKey{}, "synthetic-run-a")
	ctxB := context.WithValue(t.Context(), contextMetricRunKey{}, "synthetic-run-b")
	ids := map[string]int64{}
	for _, step := range []struct {
		name  string
		ctx   context.Context
		round int
	}{
		{"a1", ctxA, 1}, {"b1", ctxB, 1}, {"a2", ctxA, 2}, {"b2", ctxB, 2},
	} {
		id, err := store.recordContextMetricsRound(step.ctx, "synthetic policy", []map[string]string{{"role": "user", "content": step.name}}, nil, step.round)
		if err != nil || id == 0 {
			t.Fatalf("metric insert %s: id=%d err=%v", step.name, id, err)
		}
		ids[step.name] = id
	}
	for index, usage := range []deepseek.Usage{
		{Model: "synthetic-model-a", Reported: true, Input: 100, Output: 30, CacheHit: 40, CacheHitReported: true, Time: time.Now()},
		{Model: "synthetic-model-b", Reported: true, Input: 120, Output: 50, CacheHitReported: false, Time: time.Now()},
	} {
		// A failed/empty model decision may still have a billable provider usage.
		usageCtx := ctxA
		if index == 1 {
			var cancel context.CancelFunc
			usageCtx, cancel = context.WithCancel(ctxA)
			cancel()
		}
		if err := store.recordContextUsage(usageCtx, ids["a1"], usage); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.recordContextUsage(ctxB, ids["b1"], deepseek.Usage{Model: "synthetic-model-b", Reported: true, Input: 20, Output: 4, CacheHitReported: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.recordContextUsage(ctxB, ids["b2"], deepseek.Usage{}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ContextMetrics(t.Context())
	if err != nil || len(rows) != 4 {
		t.Fatalf("metric read: count=%d err=%v", len(rows), err)
	}
	got := map[string]map[string]any{}
	for _, row := range rows {
		key := row["run_id"].(string) + "/" + strconv.Itoa(row["model_round"].(int))
		got[key] = row
	}
	a1, b1, a2, b2 := got["synthetic-run-a/1"], got["synthetic-run-b/1"], got["synthetic-run-a/2"], got["synthetic-run-b/2"]
	if a1 == nil || b1 == nil || a2 == nil || b2 == nil || a1["provider_attempts"] != 2 || a1["provider_reported"] != 2 || a1["provider_input_tokens"] != int64(220) || a1["provider_output_tokens"] != int64(80) || a1["provider_cache_hit_tokens"] != int64(40) || a1["provider_cache_unknown"] != 1 || a1["provider_model"] != "mixed" {
		t.Fatalf("interleaved retry attribution failed: a1=%v", a1)
	}
	if b1["provider_attempts"] != 1 || b1["provider_cache_unknown"] != 0 || b1["provider_cache_hit_tokens"] != int64(0) || b1["provider_model"] != "synthetic-model-b" || a2["provider_attempts"] != 0 || b2["provider_attempts"] != 1 || b2["provider_reported"] != 0 {
		t.Fatalf("run isolation or cache-presence lost: b1=%v a2=%v b2=%v", b1, a2, b2)
	}
}

func TestContextMetricsOldRowsUpgradeWithoutInventingProviderUsage(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "memory.sqlite")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"DROP TABLE context_metrics",
		"CREATE TABLE context_metrics(id INTEGER PRIMARY KEY,at TEXT,total INTEGER,bytes INTEGER,components TEXT,method TEXT)",
		`INSERT INTO context_metrics(at,total,bytes,components,method) VALUES('2026-11-25T00:00:00Z',17,50,'{"policy":17}','conservative_text_estimate')`,
	} {
		if _, err := db.Exec(query); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rows, err := store.ContextMetrics(t.Context())
	if err != nil || len(rows) != 1 || rows[0]["estimated_input_tokens"] != 17 || rows[0]["run_id"] != "" || rows[0]["model_round"] != 0 || rows[0]["provider_attempts"] != 0 {
		t.Fatalf("old context row lost or reinterpreted: rows=%v err=%v", rows, err)
	}
	if _, err := store.recordContextMetricsRound(withContextMetricRun(t.Context()), "new policy", nil, nil, 1); err != nil {
		t.Fatal(err)
	}
}

func TestContextMetricsFollowActualNativeModelRounds(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	model := &overviewModel{t: t}
	e, err := NewPreview(enabledConfig(), model, "synthetic policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Store, e.Tools, e.MaxToolCalls = store, &overviewAccess{}, 2
	if _, err := e.Respond(t.Context(), Input{Mode: "direct", Text: "synthetic tool overview", Preview: true}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ContextMetrics(t.Context())
	if err != nil || len(rows) != 2 || model.steps != 2 {
		t.Fatalf("native rounds unavailable: count=%d steps=%d err=%v", len(rows), model.steps, err)
	}
	if rows[0]["model_round"] != 2 || rows[1]["model_round"] != 1 || rows[0]["run_id"] == "" || rows[0]["run_id"] != rows[1]["run_id"] {
		t.Fatalf("model decisions not bound to the same ordered run: %#v", rows)
	}
}
