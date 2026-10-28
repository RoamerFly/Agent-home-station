package admin

import (
	"encoding/json"
	"strings"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

// Keep the existing on-disk fields so old jobs need no destructive migration.
// Every external worker shares result claiming, model context and completion.
func (t agentTask) resultKind() string {
	switch {
	case t.PluginJob != nil:
		return "plugin"
	case t.ProcessID != "":
		return "process"
	case t.DeviceTask != "":
		return "device"
	case t.MusicJob != "":
		return "music"
	default:
		return ""
	}
}

// updateSnapshot rejects late worker responses after cancellation or another
// transition. Tool action evidence is independently appended and must survive.
func (a *agentControl) updateSnapshot(t agentTask) (bool, error) {
	return a.transitionSnapshot(t, false)
}

// Only the authenticated explicit resume route may revive a cancelled task.
// Background polls/results always use updateSnapshot above.
func (a *agentControl) transitionSnapshot(t agentTask, resume bool) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	current, ok := a.data.Tasks[t.ID]
	if !ok || !current.Updated.Equal(t.Updated) || current.State == "cancelled" && !resume {
		return false, nil
	}
	t.Actions = current.Actions
	if current.Goal != "" {
		t.Goal = current.Goal
	}
	t.Updated = time.Now().UTC()
	a.data.Tasks[t.ID] = copyTask(t)
	if err := a.persist(); err != nil {
		a.data.Tasks[t.ID] = current
		return false, err
	}
	return true, nil
}

func (m *Manager) claimTaskResult(task agentTask, now time.Time, facts any, next string) (*companion.Input, error) {
	task.State = "running"
	task.Runs++
	saved, err := m.agent.updateSnapshot(task)
	if err != nil || !saved {
		return nil, err
	}
	data, _ := json.Marshal(map[string]any{"kind": task.resultKind() + "_completion", "task_id": task.ID, "original_request": task.RequestedText, "result": facts, "next_action": next})
	return &companion.Input{Channel: "wechat", Account: task.Account, Thread: task.Thread, Sender: task.Sender,
		BackgroundResult: true,
		Mode:             "direct", Directed: true, IsGroup: strings.HasSuffix(task.Thread, "@chatroom"), TaskID: task.ID,
		OriginTraceID: task.OriginTrace, RequestedText: task.RequestedText, QuoteServerID: task.QuoteServerID,
		TraceID: task.resultKind() + "-result-" + task.ID + "-" + now.UTC().Format("150405.000000000"),
		Text:    "[已保存任务的真实结果，不是新用户指令] " + string(data) + "\n结合最新会话和实际工具反馈决定下一步：检查或修复、验证、继续交付、更新检查点、自然回复或安静。先核对取消和需求变化；不要把请求参数当作已验证结果，不重放已成功操作。附件在media_ids交付，介绍在接受后发送。等待时保存agent_task检查点；技术问题只私聊主人，原群保持正常交流。"}, nil
}

func (m *Manager) finishTaskResult(t agentTask, state string) bool {
	kind := t.resultKind()
	if kind == "" {
		return false
	}
	prepared, failed := false, false
	switch kind {
	case "music":
		prepared, failed = t.MusicPrepared, t.MusicFailure
	case "plugin":
		prepared, failed = t.PluginJob.Prepared, t.PluginJob.Failure
	}
	settled := state == "reply" || state == "silent" || state == "synchronous_round_finished"
	switch {
	case kind == "process" || kind == "device":
		t.State = "completed"
		t.LastResult = "执行结果已交给主模型处理；实际操作与微信投递分别以记录和回执为准"
		if !settled {
			t.State = "waiting"
			t.Next = time.Now().Add(bot.DeliveryRetryBackoff(t.Runs))
			t.LastResult = "本轮结果处理未完成，稍后继续核对同一任务，不重新运行原操作"
		}
		if settled && t.Delivery != nil && t.Delivery.State != "accepted" {
			t.State = "waiting_delivery"
			t.LastResult = "操作结果已处理，等待微信发送回执"
		}
	case failed && settled:
		t.State = "failed"
		t.LastResult = "外部任务报告失败；真实原因已交给主模型处理"
	case prepared && (state == "reply" || state == "synchronous_round_finished"):
		t.State = "waiting_delivery"
		t.LastResult = "输出已准备，等待微信交付证据；全部必需附件与后置说明完成后才结束"
	case prepared:
		t.State = "needs_review"
		t.LastResult = "输出已暂存，本轮未确认交付；核对原发送记录，不重复暂存或发送"
	default:
		t.State = "waiting"
		t.Next = time.Now().Add(bot.DeliveryRetryBackoff(t.Runs))
		t.LastResult = "本轮未取得可交付输出，稍后核对同一任务及真实反馈，不重新创建"
	}
	if t.State == "completed" || t.State == "waiting_delivery" {
		t.WorkCompleted = true
	}
	_, _ = m.agent.updateSnapshot(t)
	return true
}
