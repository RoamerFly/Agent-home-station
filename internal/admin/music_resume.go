package admin

import (
	"context"
	"encoding/json"
	"time"
)

// The caller has checked the durable account/conversation job binding. This
// action only resumes local computation; it never stages or sends an attachment.
func (p *permittedTools) resumeRVCCover(ctx context.Context, id string, status map[string]any) (string, error) {
	if status["engine"] != "rvc" {
		return "", toolInputError("仅可继续原RVC计算；YuE任务需先核查原后端执行状态")
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
		data, _, err := p.yueRequest(ctx, "POST", "/jobs/"+id+"/resume", map[string]any{}, "")
		if err != nil {
			return "", err
		}
		var resumed map[string]any
		if json.Unmarshal(data, &resumed) != nil || resumed["id"] != id || resumed["engine"] != "rvc" {
			return "", toolInputError("原RVC任务恢复响应身份无效，请查询同一个job_id")
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
			task.Checkpoint = "继续原RVC计算，核对同一个job_id；按现有来源和目标交付，不重复转换或重复发送"
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
