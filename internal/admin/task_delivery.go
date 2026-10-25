package admin

import (
	"fmt"
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
			task.LastResult += "；所需附件与介绍已完成交付"
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
		m.record(bot.Record{Event: "task_delivery_updated", Category: "decision", ThreadID: in.Thread, MessageID: task.OriginTrace, Status: task.State, Reason: "task_delivery_" + delivery.State, Content: task.LastResult, Origin: "agent"})
		return nil
	}
	return nil
}
