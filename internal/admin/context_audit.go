package admin

import (
	"fmt"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

func (p *permittedTools) ContextPrepared(r companion.ContextReport) {
	if p.trace.Preview {
		return
	}
	policy := "无歌曲工具"
	if r.MusicPolicy {
		policy = "歌曲策略v2"
	}
	p.manager.record(bot.Record{Event: "model_context_prepared", Category: "decision", ThreadID: p.trace.Thread, MessageID: p.trace.TraceID,
		Content: fmt.Sprintf("模型上下文：系统%d字节；输入%d字节；历史%d条（节选%d）；摘要%t；工作区%t；当前请求%t；可见工具%d；%s", r.SystemBytes, r.InputBytes, r.HistoryMessages, r.HistoryExcerpts, r.HasSummary, r.HasWorkspace, r.HasCurrent, r.ActiveTools, policy), Status: "prepared", Reason: "context_v2"})
}
