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
		})
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
