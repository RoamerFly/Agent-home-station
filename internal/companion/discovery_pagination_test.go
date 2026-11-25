package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"wechat-bot/internal/mcptools"
)

type pagedCatalog struct{ calls int }

func (*pagedCatalog) List(context.Context) ([]mcptools.Tool, error) {
	tools := []mcptools.Tool{}
	for i := 0; i < 12; i++ {
		tools = append(tools, mcptools.Tool{Name: fmt.Sprintf("catalog_%02d", i), Description: "catalog inspection", InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
	return tools, nil
}

type cacheMissMusicTools struct{ calls []string }

func (*cacheMissMusicTools) List(context.Context) ([]mcptools.Tool, error) {
	var tools []mcptools.Tool
	for _, name := range []string{"media_recent", "web_audio_search", "web_media_download"} {
		tools = append(tools, mcptools.Tool{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
	return tools, nil
}

func (p *cacheMissMusicTools) Call(_ context.Context, name string, _ json.RawMessage) (string, error) {
	p.calls = append(p.calls, name)
	switch name {
	case "media_recent":
		return `{"items":[],"total":0}`, nil
	case "web_audio_search":
		return `{"items":[{"url":"https://www.bilibili.com/video/BV1xx411c7mD","title":"合成样本"}]}`, nil
	case "web_media_download":
		return `{"media":[{"id":"prepared-fixture"}]}`, nil
	}
	return "", fmt.Errorf("unexpected tool %s", name)
}

type musicSearchModel struct {
	t    *testing.T
	step int
}

func (*musicSearchModel) Chat(context.Context, string, []map[string]string) (string, error) {
	panic("expected native tools")
}
func (m *musicSearchModel) ChatFunctions(_ context.Context, _ string, _ []map[string]string, tools []mcptools.Tool) (string, error) {
	m.step++
	actions := []string{"discover_tools", "media_recent", "web_audio_search", "web_media_download"}
	args := []string{`{"query":"media_recent"}`, `{"query":"合成样本"}`, `{"query":"合成样本"}`, `{"kind":"voice","url":"https://www.bilibili.com/video/BV1xx411c7mD"}`}
	if m.step > len(actions) {
		return `{"action":"reply","messages":["已找到对应录音。"]}`, nil
	}
	name := actions[m.step-1]
	if !slices.ContainsFunc(tools, func(t mcptools.Tool) bool { return t.Name == name }) {
		m.t.Fatalf("missing next action %s", name)
	}
	return fmt.Sprintf(`{"action":"tool","tool":%q,"arguments":%s}`, name, args[m.step-1]), nil
}

func TestAudioRequestCanSearchAndDownloadAfterEmptyCacheWithoutUserURL(t *testing.T) {
	tools := &cacheMissMusicTools{}
	engine, err := NewPreview(enabledConfig(), &musicSearchModel{t: t}, "base persona")
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools, engine.MaxToolCalls = tools, 6
	if _, err = engine.Respond(t.Context(), Input{Mode: "direct", Preview: true, Text: "帮我找一首歌，曲名是合成样本"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tools.calls, []string{"media_recent", "web_audio_search", "web_media_download"}) {
		t.Fatalf("calls: %v", tools.calls)
	}
	if emptyAssetSearch(`{"items":[],"total":8,"next_offset":8}`) {
		t.Fatal("last page misreported as cache miss")
	}
}

type savedPageModel struct{ step int }

func (*savedPageModel) Chat(context.Context, string, []map[string]string) (string, error) {
	panic("expected native tools")
}
func (m *savedPageModel) ChatFunctions(context.Context, string, []map[string]string, []mcptools.Tool) (string, error) {
	m.step++
	if m.step == 1 {
		return `{"action":"tool","tool":"discover_tools","arguments":{"query":"web_media_download"}}`, nil
	}
	if m.step == 2 {
		return `{"action":"tool","tool":"web_media_download","arguments":{"kind":"file","url":"https://www.bilibili.com/video/BV1xx411c7mD/","audio_only":true}}`, nil
	}
	return `{"action":"reply","messages":["已检查保存的公开页面。"]}`, nil
}

func TestSavedPublicPageCanBeRetriedWithoutNewSearch(t *testing.T) {
	tools := &cacheMissMusicTools{}
	engine, err := NewPreview(enabledConfig(), &savedPageModel{}, "base persona")
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools, engine.MaxToolCalls = tools, 4
	if _, err = engine.Respond(t.Context(), Input{Mode: "direct", Preview: true, Text: "继续上次保存的音频任务"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tools.calls, []string{"web_media_download"}) {
		t.Fatalf("saved page was blocked or needlessly searched: %v", tools.calls)
	}
}
func (p *pagedCatalog) Call(context.Context, string, json.RawMessage) (string, error) {
	p.calls++
	return "real fixture execution", nil
}

type catalogModel struct {
	t    *testing.T
	step int
}

func (*catalogModel) Chat(context.Context, string, []map[string]string) (string, error) {
	panic("native functions required")
}
func (m *catalogModel) ChatFunctions(_ context.Context, _ string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
	m.step++
	has := func(name string) bool {
		for _, t := range tools {
			if t.Name == name {
				return true
			}
		}
		return false
	}
	switch m.step {
	case 1:
		return `{"action":"tool","tool":"discover_tools","arguments":{"query":"catalog","limit":7}}`, nil
	case 2:
		if !has("catalog_06") || has("catalog_11") {
			m.t.Fatal("first discovery page leaked other schemas")
		}
		last := turns[len(turns)-1]["content"]
		if !strings.Contains(last, "next_native_function_catalog") || strings.Contains(last, "input_schema") {
			m.t.Fatal("discovery duplicated native schemas in its result", last)
		}
		return `{"action":"tool","tool":"discover_tools","arguments":{"query":"catalog","offset":7,"limit":7}}`, nil
	case 3:
		if !has("catalog_11") || has("catalog_06") {
			m.t.Fatal("later page missing or old schemas kept across pages")
		}
		return `{"action":"tool","tool":"catalog_11","arguments":{}}`, nil
	default:
		return `{"action":"reply","messages":["已核对"]}`, nil
	}
}
func TestNativeToolDiscoveryContinuesBeyondFirstEightMatches(t *testing.T) {
	tools := &pagedCatalog{}
	model := &catalogModel{t: t}
	e, _ := NewPreview(enabledConfig(), model, "policy")
	e.Tools = tools
	e.MaxToolCalls = 6
	if _, err := e.Respond(context.Background(), Input{Mode: "direct", Text: "核对工具目录", Preview: true}); err != nil || tools.calls != 1 {
		t.Fatal(err, tools.calls)
	}
}

type discoveryBatchModel struct {
	t    *testing.T
	step int
}

func (*discoveryBatchModel) Chat(context.Context, string, []map[string]string) (string, error) {
	panic("native functions required")
}

func (m *discoveryBatchModel) ChatFunctions(_ context.Context, _ string, _ []map[string]string, tools []mcptools.Tool) (string, error) {
	m.step++
	switch m.step {
	case 1:
		return `{"action":"tool_batch","calls":[{"action":"tool","tool":"discover_tools","arguments":{"query":"catalog","offset":0,"limit":6}},{"action":"tool","tool":"discover_tools","arguments":{"query":"catalog","offset":6,"limit":6}}]}`, nil
	case 2:
		if !slices.ContainsFunc(tools, func(tool mcptools.Tool) bool { return tool.Name == "catalog_01" }) ||
			!slices.ContainsFunc(tools, func(tool mcptools.Tool) bool { return tool.Name == "catalog_11" }) {
			m.t.Fatal("same-batch discovery pages were not combined")
		}
		return `{"action":"tool","tool":"catalog_01","arguments":{}}`, nil
	default:
		return `{"action":"reply","messages":["已核对"]}`, nil
	}
}

func TestNativeToolDiscoveryCombinesPagesWithinOneBatch(t *testing.T) {
	access := &pagedCatalog{}
	model := &discoveryBatchModel{t: t}
	e, err := NewPreview(enabledConfig(), model, "policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools, e.MaxToolCalls = access, 5
	if _, err := e.Respond(t.Context(), Input{Mode: "direct", Text: "查目录", Preview: true}); err != nil || access.calls != 1 || model.step != 3 {
		t.Fatalf("batch discovery failed: err=%v calls=%d model_steps=%d", err, access.calls, model.step)
	}
}

type largePageReplayModel struct {
	t    *testing.T
	step int
}

func (*largePageReplayModel) Chat(context.Context, string, []map[string]string) (string, error) {
	panic("native functions required")
}

func (m *largePageReplayModel) ChatFunctions(_ context.Context, _ string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
	const firstPage = `{"action":"tool","tool":"discover_tools","arguments":{"query":"tool_","limit":140}}`
	m.step++
	switch m.step {
	case 1:
		return firstPage, nil
	case 2:
		var feedback struct{ Result string }
		if err := json.Unmarshal([]byte(turns[len(turns)-1]["content"]), &feedback); err != nil || len(feedback.Result) <= 1500 {
			m.t.Fatalf("discovery fixture must exceed the web result cache limit: size=%d err=%v", len(feedback.Result), err)
		}
		return firstPage, nil
	case 3:
		if !strings.Contains(turns[len(turns)-1]["content"], `"status":"blocked"`) {
			m.t.Fatal("same large page was executed again while its tools were active")
		}
		return `{"action":"tool","tool":"discover_tools","arguments":{"query":"tool_","offset":140,"limit":140}}`, nil
	case 4:
		if !slices.ContainsFunc(tools, func(tool mcptools.Tool) bool { return tool.Name == "tool_149" }) {
			m.t.Fatal("second discovery page did not expose its tools")
		}
		return firstPage, nil
	case 5:
		if !slices.ContainsFunc(tools, func(tool mcptools.Tool) bool { return tool.Name == "tool_139" }) {
			m.t.Fatal("returning to the first discovery page did not re-expose its tools")
		}
		return `{"action":"tool","tool":"tool_139","arguments":{}}`, nil
	default:
		if !strings.Contains(turns[len(turns)-1]["content"], `"status":"succeeded"`) {
			m.t.Fatal("execution did not resume after discovery of the earlier page")
		}
		return `{"action":"reply","messages":["已核对"]}`, nil
	}
}

func TestNativeToolDiscoveryBlocksRepeatedLargePageAndContinues(t *testing.T) {
	model := &largePageReplayModel{t: t}
	e, err := NewPreview(enabledConfig(), model, "policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools, e.MaxToolCalls = largeDiscoveryTools{}, 6
	decision, err := e.Respond(t.Context(), Input{Mode: "direct", Text: "核对工具目录", Preview: true})
	if err != nil || decision.Action != "reply" || model.step != 6 {
		t.Fatalf("decision=%+v err=%v steps=%d", decision, err, model.step)
	}
}

func TestDiscoveryRanksExactNamesAndChineseActions(t *testing.T) {
	catalog := []mcptools.Tool{
		{Name: "aaa_help", Description: "If needed, use web_media_download"},
		{Name: "web_media_download", Description: "下载音频或视频，使用已找到的公开网址"},
	}
	for _, query := range []string{"web_media_download", "从公开网页下载音频"} {
		matches := discoverToolMatches(catalog, query)
		if len(matches) == 0 || matches[0].Name != "web_media_download" {
			t.Fatalf("query %q: %v", query, matches)
		}
	}
	if len(discoverToolMatches(catalog, "unlisted-plugin")) != 0 {
		t.Fatal("unexpected tool match")
	}
	if resourceOperation("service_plugin_call", json.RawMessage(`{"plugin":"web-search","capability":"web.search"}`)) != "web_search" ||
		resourceOperation("service_plugin_call", json.RawMessage(`{"plugin":"other","capability":"web.search"}`)) == "web_search" {
		t.Fatal("incorrect web operation accounting")
	}
}

func TestPublicIndexRetryOnlyAfterUnavailableSources(t *testing.T) {
	for _, tc := range []struct {
		code     string
		attempts int
	}{
		{"search_sources_unavailable", 2}, {"search_no_relevant_results", 1},
	} {
		n := 0
		_, err := retryReadOnlyTool(t.Context(), func(ctx context.Context) (string, error) {
			if RetryingPublicIndex(ctx) != (n > 0) {
				t.Fatal("incorrect retry identity", n)
			}
			n++
			return "", &mcptools.WebError{Code: tc.code, Stage: "plugin"}
		})
		if err == nil || n != tc.attempts {
			t.Fatalf("%s: attempts=%d", tc.code, n)
		}
	}
}
