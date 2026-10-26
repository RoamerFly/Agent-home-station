package admin

import (
	"fmt"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

func (m *Manager) updateAgentDelivery(in companion.Input, delivery bot.TaskDelivery) error {
	for _, task := range m.agent.list(in.Account, in.Thread) {
		if task.ID != in.TaskID || task.MusicJob == "" && task.PluginJob == nil && task.RecoveryMessageID == "" || task.State == "running" || task.State == "cancelled" {
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
			reportTo := ""
			for _, owner := range m.agent.config().OwnerIDs {
				if permittedRecipient(m.Config().Config, owner) {
					reportTo = owner
					if owner == in.Sender {
						break
					}
				}
			}
			if reportTo != "" {
				id := agentID(in.Account, in.Thread, delivery.Trace, delivery.PendingJob, "delivery-recovery")
				exists := false
				for _, old := range m.agent.list(in.Account, "") {
					exists = exists || old.ID == id
				}
				if !exists {
					recovery := agentTask{DeliveryJob: delivery.PendingJob, ID: id, Account: in.Account, Thread: reportTo, ReportSourceThread: in.Thread, Sender: in.Sender, OwnerRequested: access.privileged(), ReportOnly: !access.privileged(), Title: "处理发送异常", State: "waiting", Next: time.Now().Add(30 * time.Second), OriginTrace: task.OriginTrace, RequestedText: task.RequestedText, Checkpoint: fmt.Sprintf("技术进展只发当前主人私聊，原群只补发正常内容。来源会话 %s；原发送任务 %s，已接受语音 %d/%d 段，原因 %s。读取wechat_delivery_status及该群实际聊天记录；retryable=false不代表不能自主恢复，调用wechat_delivery_retry核对聊天后续办同一任务，保留已接受分段，不重新生成歌曲。失败要检查具体原因、项目文档并修复验证；等待时agent_task保存检查点，不以一次失败结束。", in.Thread, delivery.PendingJob, delivery.AcceptedParts, delivery.TotalParts, delivery.FailureReason)}
					if recovery.ReportOnly {
						recovery.Sender = ""
						recovery.RequestedText = ""
						recovery.Checkpoint = fmt.Sprintf("私聊主人发送状态报告：来源会话 %s，已接受语音 %d/%d 段，原发送任务 %s，异常 %s。系统正在核对原任务。简短说明已知事实，不声称未确认部分已经送达，不向群发送技术说明；当前仅作报告，不操作文件、程序或其他会话。", in.Thread, delivery.AcceptedParts, delivery.TotalParts, delivery.PendingJob, delivery.FailureReason)
					}
					recovery.RequestedText = "" // Repair delivery, not a new music request.
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
