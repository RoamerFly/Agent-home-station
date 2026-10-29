package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/mcptools"
)

type policyContextTools struct{ reports []ContextReport }

func (p *policyContextTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "music_local_start", Description: "singing generation", InputSchema: json.RawMessage(`{"type":"object"}`)}, {Name: "web_search", Description: "search", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (p *policyContextTools) Call(context.Context, string, json.RawMessage) (string, error) {
	return `{"found":false}`, nil
}
func (p *policyContextTools) ContextPrepared(r ContextReport) { p.reports = append(p.reports, r) }

type capturePolicyModel struct {
	system string
	turns  []map[string]string
}

func (m *capturePolicyModel) Chat(_ context.Context, s string, turns []map[string]string) (string, error) {
	m.system, m.turns = s, turns
	return `{"action":"silent","messages":[]}`, nil
}
func (m *capturePolicyModel) ChatFunctions(ctx context.Context, s string, turns []map[string]string, _ []mcptools.Tool) (string, error) {
	return m.Chat(ctx, s, turns)
}

func TestNativeContextKeepsCommonGuidanceCurrentRequestAndStableSongPolicy(t *testing.T) {
	model, tools := &capturePolicyModel{}, &policyContextTools{}
	e, err := NewPreview(enabledConfig(), model, "base persona")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = tools
	_, err = e.Respond(context.Background(), Input{Mode: "direct", Text: "给我发一首轻快的音乐", Preview: true, TraceID: "current"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.system, "先检索本地目录中的匹配原录音") || !strings.Contains(model.system, "【歌曲演唱策略 v3】") || strings.Contains(model.system, "music生成是原创演唱") {
		t.Fatal("shared guidance or current song policy was overwritten")
	}
	if !strings.Contains(model.system, "翻唱默认RVC直接换声") || strings.Contains(model.system, "所有唱歌默认 YuE") {
		t.Fatal("new RVC default missing or overridden by stale policy")
	}
	if len(tools.reports) != 1 || !tools.reports[0].MusicPolicy || !tools.reports[0].HasCurrent || tools.reports[0].ActiveTools == 0 {
		t.Fatal(tools.reports)
	}
	if !strings.Contains(model.turns[len(model.turns)-1]["content"], "current_message") {
		t.Fatal("current request not last at model boundary")
	}
}

func TestContextCompactionDoesNotRemoveSystemSongPolicyOrCurrentRequest(t *testing.T) {
	turns := []map[string]string{{"role": "user", "content": `{"kind":"conversation_context","summary":null,"timeline":[{"text":"old male recording"}]}`}, {"role": "user", "content": `{"kind":"current_message","text":"唱原曲"}`}}
	for i := 0; i < 40; i++ {
		turns = append(turns, map[string]string{"role": "user", "content": strings.Repeat("tool history ", 1800)})
	}
	kept := compactActionTurns(musicPolicy, turns)
	r := inspectContext(musicPolicy, kept, nil)
	if !r.MusicPolicy || !r.HasCurrent || r.HasSummary {
		t.Fatal(r)
	}
}

func TestLongHistoricalVideoDoesNotEvictEarlierPreferenceAndMemoryIsUnchanged(t *testing.T) {
	cfg := enabledConfig()
	cfg.Permissions.Remember = true
	cfg.Context = &ContextSettings{false, 120, 60, 8000}
	store, _ := NewStore(t.TempDir())
	e, _ := New(cfg, store, &capturePolicyModel{}, "policy")
	full := strings.Repeat("这是一段很长的视频观察。", 3000)
	scope := Scope{Messages: []Turn{
		{Role: "user", TraceID: "preference", Text: "唱歌默认使用女声", Time: time.Now().Add(-time.Minute)},
		{Role: "user", TraceID: "video", Text: full, Time: time.Now()},
	}}
	turns := e.contextTurns(scope, Input{TraceID: "new"})
	if len(turns) != 1 || !strings.Contains(turns[0]["content"], "唱歌默认使用女声") || !strings.Contains(turns[0]["content"], `"text_excerpted":true`) || scope.Messages[1].Text != full {
		t.Fatal("long observation evicted the preference or changed stored memory")
	}
	r := inspectContext(musicPolicy, turns, nil)
	if r.HistoryMessages != 2 || r.HistoryExcerpts != 1 {
		t.Fatal(r)
	}
}
