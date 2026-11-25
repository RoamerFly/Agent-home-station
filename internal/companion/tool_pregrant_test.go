package companion

import (
	"context"
	"encoding/json"
	"testing"
	"wechat-bot/internal/mcptools"
)

type pregrantAccess struct{ grant bool }

func (a pregrantAccess) List(context.Context) ([]mcptools.Tool, error) {
	if !a.grant {
		return nil, nil
	}
	return []mcptools.Tool{{Name: "incoming_media_read", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (pregrantAccess) Call(context.Context, string, json.RawMessage) (string, error) {
	return "", nil
}

type pregrantModel struct{ firstTools []mcptools.Tool }

func (*pregrantModel) Chat(context.Context, string, []map[string]string) (string, error) {
	panic("native functions required")
}
func (m *pregrantModel) ChatFunctions(_ context.Context, _ string, _ []map[string]string, tools []mcptools.Tool) (string, error) {
	if m.firstTools == nil {
		m.firstTools = append([]mcptools.Tool{}, tools...)
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
