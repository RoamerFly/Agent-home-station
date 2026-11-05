package admin

import (
	"context"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

func (p *permittedTools) mcpContext(ctx context.Context) context.Context {
	if p.trace.Preview || p.trace.Thread == "" {
		return ctx
	}
	return mcptools.WithSessionScope(ctx, companion.ScopeID(p.trace.Channel, p.trace.Account, p.trace.Thread)+":"+agentID(p.trace.Sender))
}
