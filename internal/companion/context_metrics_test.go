package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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
