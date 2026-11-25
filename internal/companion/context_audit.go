package companion

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"strings"
	"wechat-bot/internal/mcptools"
)

//go:embed policies/music.md
var musicPolicy string

// ContextReport contains counts and versions only, never prompts, messages,
// summaries, credentials or tool arguments.
type ContextReport struct {
	ComponentBytes  map[string]int
	EstimatedTokens int
	ToolSchemaBytes int
	UnreadMessages  int
	SystemBytes     int
	InputBytes      int
	HistoryMessages int
	HistoryExcerpts int
	HasSummary      bool
	HasWorkspace    bool
	HasCurrent      bool
	ActiveTools     int
	MusicPolicy     bool
}

type contextPartSize struct {
	Bytes  int
	Tokens int
}

func addContextPart(parts map[string]contextPartSize, name string, raw json.RawMessage) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "[]" {
		return
	}
	part := parts[name]
	part.Bytes += len(raw)
	part.Tokens += EstimateTokens(string(raw))
	parts[name] = part
}

// These values are only measured, never retained. Nested quote/media sizes
// overlap the timeline size and must not be added to the total input estimate.
func packetContextParts(content string) map[string]contextPartSize {
	var packet map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &packet) != nil {
		return nil
	}
	var kind string
	if json.Unmarshal(packet["kind"], &kind) != nil {
		return nil
	}
	parts := map[string]contextPartSize{}
	switch kind {
	case "conversation_context":
		addContextPart(parts, "history_summary", packet["summary"])
		addContextPart(parts, "history_timeline", packet["timeline"])
		var rows []json.RawMessage
		if json.Unmarshal(packet["timeline"], &rows) == nil {
			for _, raw := range rows {
				var row map[string]json.RawMessage
				if json.Unmarshal(raw, &row) != nil {
					continue
				}
				for _, key := range []string{"quoted_message", "quoted_source", "quote_chain", "reply_context"} {
					addContextPart(parts, "history_quotes", row[key])
				}
				for _, key := range []string{"attachment_delivery", "device_observation", "media_observations"} {
					addContextPart(parts, "history_media", row[key])
				}
			}
		}
	case "current_message":
		for _, key := range []string{"quoted_source", "quoted_message_evidence", "quoted_message_context"} {
			addContextPart(parts, "current_quotes", packet[key])
		}
		for _, key := range []string{"incoming_media", "media_observations", "recent_media_focus"} {
			addContextPart(parts, "current_media", packet[key])
		}
		addContextPart(parts, "current_unread", packet["unread_messages"])
	}
	return parts
}

// Stable opaque IDs allow comparing one tool's size across rounds without
// storing its name, description or schema in diagnostics.
func toolContextParts(catalog []byte) map[string]contextPartSize {
	var definitions []json.RawMessage
	if json.Unmarshal(catalog, &definitions) != nil {
		return nil
	}
	parts := map[string]contextPartSize{}
	for _, raw := range definitions {
		var tool struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		if json.Unmarshal(raw, &tool) != nil || tool.Name == "" {
			continue
		}
		hash := sha256.Sum256([]byte(tool.Name))
		id := hex.EncodeToString(hash[:4])
		addContextPart(parts, "tool_definition_"+id, raw)
		addContextPart(parts, "tool_input_schema_"+id, tool.InputSchema)
	}
	return parts
}

func inspectContext(system string, turns []map[string]string, tools []mcptools.Tool) ContextReport {
	r := ContextReport{SystemBytes: len(system), InputBytes: len(system), ActiveTools: len(tools), MusicPolicy: strings.Contains(system, "【歌曲演唱策略 v3】")}
	r.ComponentBytes = map[string]int{"system": len(system)}
	catalog, _ := json.Marshal(tools)
	r.ToolSchemaBytes = len(catalog)
	r.ComponentBytes["tool_schemas"] = len(catalog)
	for name, part := range toolContextParts(catalog) {
		r.ComponentBytes["detail."+name] = part.Bytes
	}
	r.EstimatedTokens = EstimateTokens(system) + EstimateTokens(string(catalog))
	for _, turn := range turns {
		r.InputBytes += len(turn["content"])
		r.EstimatedTokens += EstimateTokens(turn["content"]) + 8
		var packet struct {
			Unread   []json.RawMessage `json:"unread_messages"`
			Kind     string            `json:"kind"`
			Timeline []json.RawMessage `json:"timeline"`
			Summary  json.RawMessage   `json:"summary"`
		}
		if json.Unmarshal([]byte(turn["content"]), &packet) != nil {
			r.ComponentBytes["action_or_text"] += len(turn["content"])
			continue
		}
		r.ComponentBytes[packet.Kind] += len(turn["content"])
		for name, part := range packetContextParts(turn["content"]) {
			r.ComponentBytes["detail."+name] += part.Bytes
		}
		switch packet.Kind {
		case "conversation_context":
			r.HistoryMessages += len(packet.Timeline)
			for _, row := range packet.Timeline {
				var value struct {
					Excerpted bool `json:"text_excerpted"`
				}
				if json.Unmarshal(row, &value) == nil && value.Excerpted {
					r.HistoryExcerpts++
				}
			}
			r.HasSummary = len(packet.Summary) > 0 && string(packet.Summary) != "null"
		case "conversation_summary":
			r.HasSummary = true
		case "history_message", "history_reply":
			r.HistoryMessages++
		case "agent_workspace_context":
			r.HasWorkspace = true
		case "current_message":
			r.HasCurrent = true
			r.UnreadMessages = len(packet.Unread)
		}
	}
	return r
}

func (e *AgentRunner) reportContext(system string, turns []map[string]string, tools []mcptools.Tool) {
	if observer, ok := e.Tools.(interface{ ContextPrepared(ContextReport) }); ok {
		observer.ContextPrepared(inspectContext(system, turns, tools))
	}
}
