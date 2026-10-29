package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSummaryBudgetIgnoresIndentationAndUnicodeEscapeSpelling(t *testing.T) {
	valid := `{"version":1,"people":[],"topics":[],"current_focus":"` + strings.Repeat("事实", 700) + `","media":[],"pending_tasks":[],"preferences":[],"corrections":[]}`
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(valid), "", strings.Repeat(" ", 500)); err != nil {
		t.Fatal(err)
	}
	escaped := strings.ReplaceAll(strings.ReplaceAll(valid, "事", `\u4e8b`), "实", `\u5b9e`)
	for _, raw := range []string{pretty.String(), escaped} {
		if _, err := parseContextSummary(raw, true); err != nil {
			t.Fatal("equivalent valid summary rejected", err)
		}
	}
}

func TestSummarySchemaRejectsScalarsUnknownFieldsAndOversize(t *testing.T) {
	valid := `{"version":1,"people":[],"topics":[],"current_focus":"最新歌曲","media":[],"pending_tasks":[],"preferences":[],"corrections":[]}`
	for _, raw := range []string{`null`, `[]`, `"pretend summary"`, `{"version":1}`, strings.Replace(valid, `"version":1`, `"version":2`, 1), strings.Replace(valid, `"version":1`, `"tool_request":"execute", "version":1`, 1), strings.Replace(valid, "最新歌曲", strings.Repeat("长", 3501), 1)} {
		if _, err := parseContextSummary(raw, true); err == nil {
			t.Fatal("invalid summary accepted")
		}
	}
	if _, err := parseContextSummary(valid, true); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidCompressionPreservesArchiveAndCoverage(t *testing.T) {
	cfg := enabledConfig()
	cfg.Permissions.Remember = true
	cfg.Context = &ContextSettings{true, 20, 5, 4000}
	s, _ := NewStore(t.TempDir())
	in := Input{Channel: "wechat", Account: "a", Thread: "one"}
	for i := 0; i < 25; i++ {
		in.TraceID = fmt.Sprint(i)
		in.Text = fmt.Sprintf("original-%d", i)
		if err := s.Observe(in, cfg, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	e, _ := New(cfg, s, &fakeModel{answer: `[]`}, "policy")
	changed, err := e.CompressContext(context.Background(), in, true)
	if changed || err == nil {
		t.Fatal("invalid compression committed", changed, err)
	}
	scope, _ := s.Read(ScopeID("wechat", "a", "one"), cfg, time.Now())
	if len(scope.Messages) != 25 || scope.CompressedCount != 0 || scope.Summary != "" {
		t.Fatal("original memory or coverage changed")
	}
}

func TestContextBudgetAppliesToNativeAndFallbackWithSummary(t *testing.T) {
	cfg := enabledConfig()
	cfg.Context = &ContextSettings{true, 20, 10, 4000}
	scope := Scope{Summary: `{"version":1,"people":[],"topics":[],"current_focus":"最近资源","media":[],"pending_tasks":[],"preferences":[],"corrections":[]}`}
	for i := 0; i < 10; i++ {
		scope.Messages = append(scope.Messages, Turn{Role: "user", Text: fmt.Sprintf("row-%02d:", i) + strings.Repeat("聊", 400), Time: time.Unix(int64(i), 0)})
	}
	for _, model := range []LanguageModel{&fakeModel{}, &contextModel{}} {
		s, _ := NewStore(t.TempDir())
		e, _ := New(cfg, s, model, "policy")
		turns := e.contextTurns(scope, Input{})
		size := 0
		all := ""
		for _, row := range turns {
			size += len(row["content"])
			all += row["content"]
			if !json.Valid([]byte(row["content"])) {
				t.Fatal("invalid envelope")
			}
		}
		if size > 4000 || !strings.Contains(all, "row-09:") || strings.Contains(all, "row-00:") || !strings.Contains(all, "最近资源") {
			t.Fatal("budget, recency or summary lost", size)
		}
	}
}

func TestLegacySummaryRemainsAvailableWithoutReopeningCompressedMessages(t *testing.T) {
	legacy := `{"people":{"old-user":"friend"},"current_focus":"原音频请求","pending_tasks":["等待交付"],"media":[{"original_url":"https://example.org/original"}]}`
	got := budgetSummary(legacy, 2000)
	if got != legacy {
		t.Fatal("legacy compressed facts silently dropped")
	}
	if got := budgetSummary(`{"system_prompt":"arbitrary"}`, 2000); got != "null" {
		t.Fatal("unknown summary structure accepted")
	}
}
