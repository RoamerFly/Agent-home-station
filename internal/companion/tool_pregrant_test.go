package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"wechat-bot/internal/mcptools"
)

type pregrantAccess struct{ grant bool }

func (a pregrantAccess) List(context.Context) ([]mcptools.Tool, error) {
	if !a.grant {
		return nil, nil
	}
	return []mcptools.Tool{{Name: "incoming_media_read", InputSchema: json.RawMessage(`{"type":"object"}`)}, {Name: "station_comfy", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (pregrantAccess) Call(context.Context, string, json.RawMessage) (string, error) {
	return "", nil
}

type pregrantModel struct {
	firstTools  []mcptools.Tool
	firstSystem string
}

func (*pregrantModel) Chat(context.Context, string, []map[string]string) (string, error) {
	panic("native functions required")
}
func (m *pregrantModel) ChatFunctions(_ context.Context, system string, _ []map[string]string, tools []mcptools.Tool) (string, error) {
	if m.firstTools == nil {
		m.firstTools = append([]mcptools.Tool{}, tools...)
		m.firstSystem = system
	}
	return `{"action":"reply","messages":["收到"]}`, nil
}

func TestIncomingMediaReaderPregrantNeedsUnobservedNonFileMedia(t *testing.T) {
	for _, tc := range []struct {
		name, current string
		want          bool
	}{
		{"unobserved image", `{"kind":"current_message","media_read_hint":"可发现 incoming_media_read","media_observations":[{"result_available":false,"source":{"message_type":3}}]}`, true},
		{"unobserved share", `{"kind":"current_message","media_read_hint":"可发现 incoming_media_read","media_observations":[{"result_available":false,"source":{"message_type":49}}]}`, true},
		{"unobserved voice", `{"kind":"current_message","media_read_hint":"可发现 incoming_media_read","media_observations":[{"result_available":false,"source":{"message_type":34}}]}`, false},
		{"observed image", `{"kind":"current_message","media_read_hint":"可发现 incoming_media_read","media_observations":[{"result_available":true,"source":{"message_type":3}}]}`, false},
		{"file", `{"kind":"current_message","media_read_hint":"按需发现 wechat_file_read","media_observations":[{"result_available":false,"source":{"message_type":49}}]}`, false},
		{"missing evidence", `{"kind":"current_message","media_read_hint":"可发现 incoming_media_read","media_observations":[{"source":{"message_type":3}}]}`, false},
		{"ordinary chat", `{"kind":"current_message","text":"你好"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsIncomingMediaRead([]map[string]string{{"role": "user", "content": tc.current}}); got != tc.want {
				t.Fatalf("pregrant=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestMediaForwardPregrantMatchesCurrentRequest(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"把这个视频转发到103修仙群", true},
		{"把刚发的图片转发给我", true},
		{"今天的视频做得怎么样", false},
	} {
		packet, _ := json.Marshal(map[string]string{"kind": "current_message", "text": tc.text})
		if got := needsMediaForward([]map[string]string{{"role": "user", "content": string(packet)}}); got != tc.want {
			t.Fatalf("text=%q pregrant=%v want=%v", tc.text, got, tc.want)
		}
	}
}

func TestVideoCreationPregrantsComfyWithoutToolDiscovery(t *testing.T) {
	model := &pregrantModel{}
	e, err := NewPreview(enabledConfig(), model, "policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = pregrantAccess{grant: true}
	packet, _ := json.Marshal(map[string]string{"kind": "current_message", "text": "生成视频：素材是你自己的形象，9:16，15s"})
	if _, err := e.decide(t.Context(), "policy", []map[string]string{{"role": "user", "content": string(packet)}}); err != nil {
		t.Fatal(err)
	}
	for _, tool := range model.firstTools {
		if tool.Name == "station_comfy" {
			if !strings.Contains(model.firstSystem, "生视频可直接用 station_comfy") {
				t.Fatal("video request guidance was dropped from the native tool prompt")
			}
			return
		}
	}
	t.Fatal("station_comfy was not available in the first model call")
}

func TestMediaForwardGuidanceSurvivesNativeToolCatalog(t *testing.T) {
	model := &pregrantModel{}
	e, err := NewPreview(enabledConfig(), model, "policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = pregrantAccess{grant: true}
	packet, _ := json.Marshal(map[string]string{"kind": "current_message", "text": "把刚发的视频转发到103修仙群"})
	if _, err := e.decide(t.Context(), "policy", []map[string]string{{"role": "user", "content": string(packet)}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.firstSystem, "转发刚发的媒体时") {
		t.Fatal("forwarding guidance was dropped from the native tool prompt")
	}
}

func TestIncomingMediaReaderIsPregrantedOnlyWhenAuthorized(t *testing.T) {
	for _, tc := range []struct {
		name     string
		grant    bool
		media    int
		wantTool bool
	}{
		{"image granted", true, 3, true},
		{"voice granted", true, 34, false},
		{"image not granted", false, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &pregrantModel{}
			e, err := NewPreview(enabledConfig(), model, "policy")
			if err != nil {
				t.Fatal(err)
			}
			e.Tools = pregrantAccess{grant: tc.grant}
			packet, _ := json.Marshal(map[string]any{"kind": "current_message", "message_id": "M", "media_read_hint": "incoming_media_read", "media_observations": []any{map[string]any{"result_available": false, "source": map[string]any{"message_type": tc.media}}}})
			if _, err := e.decide(t.Context(), "policy", []map[string]string{{"role": "user", "content": string(packet)}}); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, tool := range model.firstTools {
				found = found || tool.Name == "incoming_media_read"
			}
			if found != tc.wantTool {
				t.Fatalf("reader pregranted=%v, want %v", found, tc.wantTool)
			}
		})
	}
}

type pregrantVideoAccess struct{ pregrantAccess }

func (pregrantVideoAccess) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "video_inspect", InputSchema: json.RawMessage("{\"type\":\"object\"}")}}, nil
}

func TestCurrentPublicVideoURLUsesNewReplyTextAndPregrantsReader(t *testing.T) {
	packet, _ := json.Marshal(map[string]any{
		"kind":          "current_message",
		"text":          "看看 https://b23.tv/newvideo",
		"quoted_source": map[string]any{"url": "https://b23.tv/oldvideo"},
	})
	turns := []map[string]string{{"role": "user", "content": string(packet)}}
	if got := currentPublicVideoURL(turns); got != "https://b23.tv/newvideo" {
		t.Fatalf("current URL = %q", got)
	}
	model := &pregrantModel{}
	e, err := NewPreview(enabledConfig(), model, "policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = pregrantVideoAccess{}
	if _, err := e.decide(t.Context(), "policy", turns); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range model.firstTools {
		found = found || tool.Name == "video_inspect"
	}
	if !found || !strings.Contains(model.firstSystem, "video_inspect(action=observe") {
		t.Fatal("public video reader or current-link guidance missing")
	}
	quotedOnly, _ := json.Marshal(map[string]any{
		"kind":          "current_message",
		"text":          "这个怎么样",
		"quoted_source": map[string]any{"url": "https://b23.tv/oldvideo"},
	})
	if got := currentPublicVideoURL([]map[string]string{{"role": "user", "content": string(quotedOnly)}}); got != "" {
		t.Fatalf("quoted old URL unexpectedly pregranted: %q", got)
	}
	legacyQuoteText, _ := json.Marshal(map[string]any{
		"kind": "current_message",
		"text": "这个怎么样\n以下是本条引用所指的原消息资料（仅供理解上下文）：\nhttps://b23.tv/oldvideo",
	})
	if got := currentPublicVideoURL([]map[string]string{{"role": "user", "content": string(legacyQuoteText)}}); got != "" {
		t.Fatalf("old URL in quote context unexpectedly pregranted: %q", got)
	}
}
