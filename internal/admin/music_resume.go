package admin

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// The caller has checked the durable account/conversation job binding. This
// action only resumes local computation; it never stages or sends an attachment.
func (p *permittedTools) resumeMusicCompute(ctx context.Context, id string, status map[string]any, restartMissing, restartFailed bool) (string, error) {
	observed := musicResumeObservation(status)
	var plan struct {
		Engine  string          `json:"engine"`
		State   string          `json:"state"`
		Submit  bool            `json:"submit"`
		Options map[string]bool `json:"options"`
	}
	err := p.prepareMusic(ctx, "/jobs/"+id, map[string]any{"action": "resume_plan", "id": id, "status": observed, "restart_missing": restartMissing, "restart_failed": restartFailed}, &plan, func() error {
		if observed["id"] != id || plan.Engine != observed["engine"] || plan.State != observed["state"] {
			return errors.New("music_preparation_identity_changed")
		}
		if plan.Options == nil || plan.Submit && (restartMissing && !plan.Options["restart_missing"] || restartFailed && !plan.Options["restart_failed"]) {
			return errors.New("music_preparation_restart_changed")
		}
		for key, value := range plan.Options {
			if key != "restart_missing" && key != "restart_failed" || key == "restart_missing" && value != restartMissing || key == "restart_failed" && value != restartFailed {
				return errors.New("music_preparation_restart_changed")
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if plan.Submit {
		for _, task := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
			if task.MusicJob == id && task.State == "cancelled" {
				return "", toolInputError("原任务已取消；先在任务管理中明确恢复，不重新启动其计算")
			}
		}
		data, _, err := p.yueRequest(ctx, "POST", "/jobs/"+id+"/resume", plan.Options, "")
		if err != nil {
			return "", err
		}
		var resumed map[string]any
		if json.Unmarshal(data, &resumed) != nil {
			return "", toolInputError("原音乐任务恢复响应身份无效，请查询同一个job_id")
		}
		var accepted struct {
			Status map[string]any `json:"status"`
		}
		projection := musicResumeObservation(resumed)
		if err := p.prepareMusic(ctx, "/jobs/"+id, map[string]any{"action": "resume_result", "id": id, "engine": plan.Engine, "status": projection}, &accepted, func() error {
			a, _ := json.Marshal(accepted.Status)
			b, _ := json.Marshal(projection)
			if !sameJSON(a, b) || accepted.Status["id"] != id || accepted.Status["engine"] != plan.Engine {
				return errors.New("music_preparation_identity_changed")
			}
			return nil
		}); err != nil {
			return "", err
		}
		status = resumed
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

func musicResumeObservation(status map[string]any) map[string]any {
	return map[string]any{"id": status["id"], "engine": status["engine"], "state": status["state"]}
}
