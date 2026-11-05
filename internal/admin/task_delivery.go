package admin

import (
	"fmt"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

// The delivery worker owns these checkpoints. Never schedule a fresh
// generation to find out whether the original side effect succeeded.
func settleFromDelivery(t *agentTask, roundReplied bool) bool {
	if t.Delivery != nil {
		switch t.Delivery.State {
		case "failed", "partial_failed":
			t.State, t.LastResult = "failed", "原投递已结束，保留已接受部分，不重发"
			return true
		case "needs_review":
			t.State, t.LastResult = "needs_review", "原投递结果尚未确定；保留原记录，不重新执行"
			return true
		case "accepted":
			if len(t.PendingOperations) == 0 {
				if t.WorkCompleted || roundReplied {
					t.State, t.WorkCompleted, t.LastResult = "completed", true, "原投递已由微信接受"
				} else {
					t.State, t.LastResult = "needs_review", "原投递已由微信接受；没有其余工作完成的记录，已停止续办"
				}
				return true
			}
		}
	}
	if t.Delivery == nil && len(t.PendingOperations) == 0 {
		return false
	}
	t.State, t.LastResult = "waiting_delivery", "Agent 将按原投递记录核对结果，不重做已执行操作"
	return true
}

func (m *Manager) updateAgentDelivery(in companion.Input, delivery bot.TaskDelivery) error {
	defer func() {
		if err := m.agent.retireSettled(); err != nil {
			m.log("task_archive_save_failed")
		}
	}()
	for _, task := range m.agent.list(in.Account, in.Thread) {
		if task.ID != in.TaskID || task.State == "cancelled" {
			continue
		}
		if task.Delivery != nil && *task.Delivery == delivery {
			return nil
		}
		if task.Delivery != nil && task.Delivery.Trace != delivery.Trace {
			return nil
		}
		task.Delivery = &delivery
		wasRunning := task.State == "running"
		previousState := task.State
		if !wasRunning {
			task.State = "waiting_delivery"
		}
		task.LastResult = fmt.Sprintf("微信已接受附件 %d/%d", delivery.Accepted, delivery.Expected)
		if delivery.TextExpected > 0 {
			task.LastResult += fmt.Sprintf("；文字 %d/%d", delivery.TextAccepted, delivery.TextExpected)
		}
		if delivery.TotalParts > 0 {
			task.LastResult += fmt.Sprintf("；语音分段 %d/%d", delivery.AcceptedParts, delivery.TotalParts)
		}
		switch delivery.State {
		case "accepted":
			if !wasRunning {
				if task.WorkCompleted || previousState == "waiting_delivery" || task.resultKind() != "" || task.RecoveryMessageID != "" || task.ReportOnly {
					task.State = "completed"
					task.WorkCompleted = true
				} else {
					task.State = previousState
				}
			}
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
			task.LastResult += "；无法确认剩余部分，保留原发送记录"
		case "checking_receipt":
			task.State = "waiting_delivery"
			task.LastResult += "；Agent 将自动查询原发送回执，不重复提交"
		}
		saved, err := m.agent.updateSnapshot(task)
		if err != nil || !saved {
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
