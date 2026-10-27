package companion

import (
	_ "embed"
	"encoding/json"
	"strings"
	"wechat-bot/internal/mcptools"
)

//go:embed policies/music.md
var musicPolicy string

// ContextReport contains counts and versions only, never prompts, messages,
// summaries, credentials or tool arguments.
type ContextReport struct {
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

func inspectContext(system string, turns []map[string]string, tools []mcptools.Tool) ContextReport {
	r := ContextReport{SystemBytes: len(system), InputBytes: len(system), ActiveTools: len(tools), MusicPolicy: strings.Contains(system, "【歌曲演唱策略 v2】")}
	for _, turn := range turns {
		r.InputBytes += len(turn["content"])
		var packet struct {
			Kind     string            `json:"kind"`
			Timeline []json.RawMessage `json:"timeline"`
			Summary  json.RawMessage   `json:"summary"`
		}
		if json.Unmarshal([]byte(turn["content"]), &packet) != nil {
			continue
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
		}
	}
	return r
}

func (e *AgentRunner) reportContext(system string, turns []map[string]string, tools []mcptools.Tool) {
	if observer, ok := e.Tools.(interface{ ContextPrepared(ContextReport) }); ok {
		observer.ContextPrepared(inspectContext(system, turns, tools))
	}
}
