package admin

import (
	"fmt"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

func (m *Manager) updateAgentDelivery(in companion.Input, delivery bot.TaskDelivery) error {
	for _, task := range m.agent.list(in.Account, in.Thread) {
		if task.ID != in.TaskID || task.MusicJob == "" && task.PluginJob == nil || task.State == "running" || task.State == "cancelled" {
			continue
		}
		if task.Delivery != nil && *task.Delivery == delivery {
			return nil
		}
		if task.Delivery != nil && task.Delivery.Trace != delivery.Trace {
			return nil
		}
		task.Delivery = &delivery
		task.State = "waiting_delivery"
		task.LastResult = fmt.Sprintf("微信已接受附件 %d/%d", delivery.Accepted, delivery.Expected)
		if delivery.TotalParts > 0 {
			task.LastResult += fmt.Sprintf("；语音分段 %d/%d", delivery.AcceptedParts, delivery.TotalParts)
		}
		switch delivery.State {
		case "accepted":
			task.State = "completed"
			if delivery.CaptionOmitted {
				task.LastResult += "；所需附件已完成交付，说明文字由模型选择省略"
			} else {
				task.LastResult += "；所需附件与介绍已完成交付"
			}
		case "failed", "partial_failed":
			task.State = "failed"
			if delivery.Expected > 0 && delivery.Accepted == delivery.Expected {
				task.LastResult += "；附件已全部接受，后续文字说明未完成；不会重复发送附件"
			} else {
				task.LastResult += "；交付中断，请查看失败项，已接受内容不重复发送"
			}
		case "needs_review":
			task.State = "needs_review"
			task.LastResult += "；存在未确认回执，请核对原发送任务"
		}
		var saveErr error
		if task.PluginJob != nil {
			var saved bool
			saved, saveErr = m.agent.pluginUpdate(task)
			if saveErr == nil && !saved {
				return nil
			}
		} else {
			saveErr = m.agent.task(task)
		}
		if err := saveErr; err != nil {
			return err
		}
		if delivery.PendingJob != "" && delivery.State == "needs_review" {
			access := &permittedTools{manager: m, trace: companion.Input{Account: in.Account, Thread: in.Thread, Sender: in.Sender}}
			if access.privileged() {
				id := agentID(in.Account, in.Thread, delivery.Trace, delivery.PendingJob, "delivery-recovery")
				exists := false
				for _, old := range m.agent.list(in.Account, in.Thread) {
					exists = exists || old.ID == id
				}
				if !exists {
					recovery := agentTask{ID: id, Account: in.Account, Thread: in.Thread, Sender: in.Sender, OwnerRequested: true, Title: "处理发送异常", State: "waiting", Next: time.Now().Add(30 * time.Second), OriginTrace: task.OriginTrace, RequestedText: task.RequestedText, Checkpoint: fmt.Sprintf("已请求任务交付异常：原发送任务 %s，已接受语音 %d/%d 段，原因 %s。先wechat_delivery_status读取真实状态；已接受部分绝不重发。若原生retryable允许，wechat_delivery_retry续传原任务，不重新生成歌曲。原生禁止重试时检查原记录与项目文档，结合主机能力排查连接/程序问题，保存检查点并向用户说明真实进展，不能只停在日志。不能把缺失回执说成确定未发送，不以新编号重发整份媒体。", delivery.PendingJob, delivery.AcceptedParts, delivery.TotalParts, delivery.FailureReason)}
					if err := m.agent.task(recovery); err != nil {
						return err
					}
				}
			}
		}
		m.record(bot.Record{Event: "task_delivery_updated", Category: "decision", ThreadID: in.Thread, MessageID: task.OriginTrace, Status: task.State, Reason: "task_delivery_" + delivery.State, Content: task.LastResult, Origin: "agent"})
		return nil
	}
	return nil
}
