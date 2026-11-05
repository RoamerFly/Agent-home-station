package admin

import (
	"encoding/json"
	"fmt"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

func (p *permittedTools) ContextPrepared(r companion.ContextReport) {
	parts, _ := json.Marshal(r.ComponentBytes)
	if p.trace.Preview {
		return
	}
	policy := "无歌曲工具"
	if r.MusicPolicy {
		policy = "歌曲策略v3"
	}
	if r.UnreadMessages > 0 {
		policy += fmt.Sprintf("；本次未读汇总%d条（各自原时间）", r.UnreadMessages)
	}
	p.manager.record(bot.Record{Event: "model_context_prepared", Category: "decision", ThreadID: p.trace.Thread, MessageID: p.trace.TraceID,
		Content: fmt.Sprintf("模型上下文：系统%d字节；输入%d字节；历史%d条（节选%d）；摘要%t；工作区%t；当前请求%t；可见工具%d；%s；估算%d Token（非账单）；各组成字节%s", r.SystemBytes, r.InputBytes, r.HistoryMessages, r.HistoryExcerpts, r.HasSummary, r.HasWorkspace, r.HasCurrent, r.ActiveTools, policy, r.EstimatedTokens, string(parts)), Status: "prepared", Reason: "context_v3"})
}
