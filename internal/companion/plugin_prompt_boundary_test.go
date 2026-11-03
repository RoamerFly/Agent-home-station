package companion

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"wechat-bot/internal/mcptools"
)

type pluginPromptCatalog struct{ tools []mcptools.Tool }

func (p *pluginPromptCatalog) List(context.Context) ([]mcptools.Tool, error) { return p.tools, nil }
func (*pluginPromptCatalog) Call(context.Context, string, json.RawMessage) (string, error) {
	panic("prompt boundary fixture must never execute a provider")
}

type pluginPromptCapture struct {
	systems  []string
	schemas  [][]mcptools.Tool
	turns    []string
	discover bool
}

func (*pluginPromptCapture) Chat(context.Context, string, []map[string]string) (string, error) {
	panic("native functions expected")
}
func (m *pluginPromptCapture) ChatFunctions(_ context.Context, system string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
	m.systems = append(m.systems, system)
	m.schemas = append(m.schemas, append([]mcptools.Tool(nil), tools...))
	packet, _ := json.Marshal(turns)
	m.turns = append(m.turns, string(packet))
	if m.discover && len(m.systems) == 1 {
		return `{"action":"tool","tool":"discover_tools","arguments":{"query":"music_local_start"}}`, nil
	}
	return `{"action":"silent","messages":[]}`, nil
}

func musicPromptCatalog(t *testing.T) []mcptools.Tool {
	t.Helper()
	raw, err := os.ReadFile("../../plugins/music/runtime/tool_catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Tools []mcptools.Tool `json:"tools"`
	}
	if err = json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	return catalog.Tools
}

func TestNativeChitchatDoesNotLoadPluginProviderTutorials(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_plugin", true: "installed_but_unrelated"}[installed], func(t *testing.T) {
			model, catalog := &pluginPromptCapture{}, &pluginPromptCatalog{}
			if installed {
				catalog.tools = musicPromptCatalog(t)
			}
			e, err := NewPreview(enabledConfig(), model, "base persona")
			if err != nil {
				t.Fatal(err)
			}
			e.Tools = catalog
			if _, err = e.Respond(context.Background(), Input{Mode: "direct", Text: "下午好", Preview: true}); err != nil {
				t.Fatal(err)
			}
			system := model.systems[0]
			t.Logf("captured system: %d runes, %d conservative estimated tokens", len([]rune(system)), EstimateTokens(system))
			for _, tutorial := range []string{"music_rvc_start", "music_local_start", "music_local_status", "voice_library_list/read", "speech_synthesize", "YuE", "GPTSoVITS", "duration_seconds", "text_guidance_scale"} {
				if strings.Contains(system, tutorial) {
					t.Errorf("unrelated system contains provider tutorial %q", tutorial)
				}
			}
			for _, core := range []string{"不要改用未指定的声音", "完整来源", "不把计划写成已完成", "只有 accepted", "每条不超过60秒", "原文件完整保留"} {
				if !strings.Contains(system, core) {
					t.Errorf("core source/voice/delivery constraint missing: %q", core)
				}
			}
			for _, instruction := range []string{"需要的能力未列出", "依据实际错误修正参数", "已取得的素材、任务和回执先查询或复用"} {
				if !strings.Contains(system, instruction) {
					t.Errorf("native discovery guidance missing: %q", instruction)
				}
			}
		})
	}
}

type guidedDiscoveryCatalog struct{ calls []string }

func (*guidedDiscoveryCatalog) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "web_audio_search", Description: "Search public audio sources by song title", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)}}, nil
}
func (c *guidedDiscoveryCatalog) Call(_ context.Context, name string, _ json.RawMessage) (string, error) {
	c.calls = append(c.calls, name)
	return `{"items":[{"title":"synthetic result"}]}`, nil
}

type guidedDiscoveryModel struct {
	t    *testing.T
	step int
}

func (*guidedDiscoveryModel) Chat(context.Context, string, []map[string]string) (string, error) {
	panic("native functions required")
}
func (m *guidedDiscoveryModel) ChatFunctions(_ context.Context, system string, _ []map[string]string, tools []mcptools.Tool) (string, error) {
	m.step++
	has := func(name string) bool {
		for _, tool := range tools {
			if tool.Name == name {
				return true
			}
		}
		return false
	}
	switch m.step {
	case 1:
		if !strings.Contains(system, nativeToolDiscoveryPolicy) || has("web_audio_search") {
			m.t.Fatal("native turn exposed provider schema before discovery or omitted discovery policy")
		}
		return `{"action":"tool","tool":"discover_tools","arguments":{"query":"audio search"}}`, nil
	case 2:
		if !has("web_audio_search") {
			m.t.Fatal("discovered audio schema not exposed")
		}
		return `{"action":"tool","tool":"web_audio_search","arguments":{"query":"synthetic song"}}`, nil
	default:
		return `{"action":"reply","messages":["找到了"]}`, nil
	}
}

func TestNativeDiscoveryGuidesSearchWithoutProviderTutorial(t *testing.T) {
	model := &guidedDiscoveryModel{t: t}
	catalog := &guidedDiscoveryCatalog{}
	e, err := NewPreview(enabledConfig(), model, "base persona")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools, e.MaxToolCalls = catalog, 3
	if _, err = e.Respond(context.Background(), Input{Mode: "direct", Text: "帮我找一首歌", Preview: true}); err != nil {
		t.Fatal(err)
	}
	if len(catalog.calls) != 1 || catalog.calls[0] != "web_audio_search" {
		t.Fatalf("discovery did not lead to one real search: %#v", catalog.calls)
	}
}

func TestMusicDiscoveryRetainsExactProviderWorkflowAndVoiceConstraints(t *testing.T) {
	model := &pluginPromptCapture{discover: true}
	catalog := &pluginPromptCatalog{tools: musicPromptCatalog(t)}
	e, err := NewPreview(enabledConfig(), model, "base persona")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools, e.MaxToolCalls = catalog, 3
	if _, err = e.Respond(context.Background(), Input{Mode: "direct", Text: "查看音乐制作能力说明，保留指定的mom声线", Preview: true}); err != nil {
		t.Fatal(err)
	}
	if len(model.schemas) != 2 {
		t.Fatalf("discovery did not continue: %d", len(model.schemas))
	}
	for _, tool := range model.schemas[0] {
		if tool.Name == "music_local_start" {
			t.Fatal("full provider schema loaded before discovery")
		}
	}
	var expected, discovered string
	for _, tool := range catalog.tools {
		if tool.Name == "music_local_start" {
			expected = tool.Description
		}
	}
	for _, tool := range model.schemas[1] {
		if tool.Name == "music_local_start" {
			discovered = tool.Description
		}
	}
	if expected == "" || discovered != expected {
		t.Fatal("discovery lost exact installed provider description")
	}
	for _, detail := range []string{"music_rvc_start", "YuE", "duration_seconds", "reference_asset_id", "automatic_followup=true", "user has already agreed", "music_local_status"} {
		if !strings.Contains(discovered, detail) {
			t.Errorf("discovered workflow missing %q", detail)
		}
	}
	var cover string
	for _, tool := range catalog.tools {
		if tool.Name == "music_rvc_start" {
			cover = tool.Description
		}
	}
	for _, detail := range []string{"voice_library_list/read", "voice_profile_id", "never silently use a default", "THIS conversation", "song/artist/version"} {
		if !strings.Contains(cover, detail) {
			t.Errorf("cover source/voice constraint missing %q", detail)
		}
	}
	if !strings.Contains(model.systems[1], "不要改用未指定的声音") {
		t.Fatal("discovery erased the requested voice constraint")
	}
	if !strings.Contains(model.turns[1], "保留指定的mom声线") {
		t.Fatal("discovery lost the current request's exact named voice")
	}
}
