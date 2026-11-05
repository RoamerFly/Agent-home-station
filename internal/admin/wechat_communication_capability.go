package admin

import (
	"strings"
	"wechat-bot/internal/mcptools"
)

// WeChat transport is a built-in host capability, not an optional document,
// voice or project plugin.  Keep its discovery in a separate group so an
// unavailable processor never makes attachment receipt, prepared-media
// delivery or receipt reconciliation disappear from the Agent's capability
// view.  Definitions remain in agentTools to preserve one call dispatcher.
func (p *permittedTools) wechatCommunicationTools() []mcptools.Tool {
	all := p.agentTools()
	out := make([]mcptools.Tool, 0, len(all))
	for _, tool := range all {
		if strings.HasPrefix(tool.Name, "wechat_") {
			out = append(out, tool)
		}
	}
	return out
}

func workspaceAgentToolName(name string) bool {
	return agentToolName(name) && !strings.HasPrefix(name, "wechat_")
}
