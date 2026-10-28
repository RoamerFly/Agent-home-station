package admin

import (
	"context"
	"encoding/json"
	"time"
)

// The caller has checked the durable account/conversation job binding. This
// action only resumes local computation; it never stages or sends an attachment.
func (p *permittedTools) resumeMusicCompute(ctx context.Context, id string, status map[string]any, restartMissing, restartFailed bool) (string, error) {
	engine, _ := status["engine"].(string)
	if engine != "rvc" && engine != "yue" {
		return "", toolInputError("原任务未提供可验证的恢复引擎，请核查桥接版本与原任务记录")
	}
	if engine == "rvc" && (restartMissing || restartFailed) {
		return "", toolInputError("restart_missing/restart_failed仅用于YuE后端任务丢失或执行失败后的原配方重算")
	}
	switch status["state"] {
	case "completed", "queued", "running":
		// A repeated resume reuses the existing compute/result, with no POST.
	case "failed", "needs_review":
		for _, task := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
			if task.MusicJob == id && task.State == "cancelled" {
				return "", toolInputError("原任务已取消；先在任务管理中明确恢复，不重新启动其计算")
			}
		}
		options := map[string]any{}
		if engine == "yue" {
			options["restart_missing"] = restartMissing
			if restartFailed {
				options["restart_failed"] = true
			}
		}
		data, _, err := p.yueRequest(ctx, "POST", "/jobs/"+id+"/resume", options, "")
		if err != nil {
			return "", err
		}
		var resumed map[string]any
		if json.Unmarshal(data, &resumed) != nil || resumed["id"] != id || resumed["engine"] != engine {
			return "", toolInputError("原音乐任务恢复响应身份无效，请查询同一个job_id")
		}
		status = resumed
	default:
		return "", toolInputError("原任务已取消或状态不支持继续，先核查原任务")
	}
	status["job_id"] = id
	status["automatic_followup"] = false
	if status["state"] == "queued" || status["state"] == "running" {
		for _, task := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
			if task.MusicJob != id || task.State == "completed" || task.State == "cancelled" {
				continue
			}
			task.State, task.MusicFailure, task.MusicPrepared = "waiting", false, false
			task.Next = time.Now().Add(20 * time.Second)
			task.Checkpoint = "继续原音乐计算，核对同一个job_id；按现有来源和目标交付，不重复生成或重复发送"
			saved, err := p.manager.agent.updateSnapshot(task)
			if err != nil {
				return "", err
			}
			if !saved {
				continue
			}
			status["automatic_followup"] = true
			status["task_id"] = task.ID
			break
		}
	}
	status["instruction"] = "保留同一个job_id；恢复仅执行本地计算，不发送音频或文字。完成后music_local_status读取真实结果，再按当前需求交付；已完成或已取消的交付任务不会重新排队。"
	output, err := json.Marshal(status)
	return string(output), err
}
