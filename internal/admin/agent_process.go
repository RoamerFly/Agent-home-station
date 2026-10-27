package admin

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
	"wechat-bot/internal/companion"
)

func (p *permittedTools) callProcess(ctx context.Context, raw json.RawMessage) (string, error) {
	return p.callProcessPlatform(ctx, raw, false)
}
func (p *permittedTools) callProcessPlatform(ctx context.Context, raw json.RawMessage, windows bool) (result string, callErr error) {
	defer func() { callErr = localAgentError(callErr) }()
	var in struct {
		Action, ID, Path, Command string
		Key                       string `json:"request_key"`
		Seconds                   int    `json:"timeout_seconds"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return "", errors.New("进程参数无效")
	}
	scope := agentID(p.trace.Account, p.trace.Thread, p.trace.Sender)
	cfg := p.manager.agent.config().Host
	if !p.privileged() || cfg.Mode != "full" {
		return "", errors.New("后台程序执行未授权")
	}
	var value any
	var e error
	switch in.Action {
	case "start":
		if !p.manager.agent.config().TasksEnabled {
			return "", errors.New("后台程序需开启已有的Agent持续任务，以便自动处理完成结果")
		}
		trace := p.trace
		allowed := func() bool {
			access := &permittedTools{manager: p.manager, trace: trace}
			current := p.manager.agent.config().Host
			return access.privileged() && current.Mode == "full" && slices.Equal(current.Roots, cfg.Roots)
		}
		toolName := "server_process"
		if windows {
			toolName = "server_windows_process"
		}
		result, err := p.privilegedCall(ctx, toolName, raw, func() (any, error) {
			if windows {
				return p.manager.processes.StartWindows(cfg, scope, in.Key, in.Path, in.Command, in.Seconds, allowed)
			}
			return p.manager.processes.Start(cfg, scope, in.Key, in.Path, in.Command, in.Seconds, allowed)
		})
		if err != nil {
			return "", err
		}
		var process struct {
			ID     string `json:"id"`
			State  string `json:"state"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal([]byte(result), &process) == nil && process.State == "rejected" {
			return "", toolInputError("程序未启动：" + process.Reason + "；更正后可再次尝试")
		}
		if json.Unmarshal([]byte(result), &process) != nil || process.ID == "" {
			return result, nil
		}
		taskID := agentID(p.trace.Account, p.trace.Thread, "process", process.ID)
		found := false
		for _, old := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
			found = found || old.ID == taskID
		}
		if !found {
			request := p.trace.RequestedText
			if request == "" {
				request = companion.UserRequestText(p.trace.Text)
			}
			origin := p.trace.OriginTraceID
			if origin == "" {
				origin = p.trace.TraceID
			}
			task := agentTask{ID: taskID, ProcessID: process.ID, Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, Title: "后台程序", RequestedText: request, Checkpoint: "等待真实程序结果，使用原进程编号查询，不重新运行命令", State: "waiting", Next: time.Now().Add(2 * time.Second), OriginTrace: origin, QuoteServerID: p.trace.QuoteServerID}
			if e = p.manager.agent.task(task); e != nil {
				_, _ = p.manager.processes.Status(scope, process.ID, true)
				b, _ := json.Marshal(map[string]any{"id": process.ID, "state": "needs_review", "automatic_followup": false, "instruction": "程序已启动，但后续任务无法保存，已请求停止。查询此进程编号核对，不重新启动。"})
				return string(b), nil
			}
		}
		var response map[string]any
		_ = json.Unmarshal([]byte(result), &response)
		response["task_id"] = taskID
		response["automatic_followup"] = true
		response["instruction"] = "进程和原会话后续任务已保存，结束后主模型将读取真实结果继续处理，不用反复轮询或另建任务。"
		b, e := json.Marshal(response)
		return string(b), e
	case "status", "cancel":
		value, e = p.manager.processes.Status(scope, in.ID, in.Action == "cancel")
	case "list":
		value = p.manager.processes.List(scope)
	default:
		return "", errors.New("进程操作须为start/status/cancel/list")
	}
	if e != nil {
		return "", e
	}
	b, e := json.Marshal(value)
	return string(b), e
}

func (m *Manager) claimProcessTask(task agentTask, now time.Time) (*companion.Input, error) {
	p := &permittedTools{manager: m, trace: companion.Input{Account: task.Account, Thread: task.Thread, Sender: task.Sender}}
	if !p.privileged() || m.agent.config().Host.Mode != "full" {
		task.State = "cancelled"
		task.LastResult = "执行权限已撤销，不交付旧进程内容"
		_, err := m.agent.updateSnapshot(task)
		return nil, err
	}
	process, err := m.processes.Status(agentID(task.Account, task.Thread, task.Sender), task.ProcessID, false)
	if err != nil {
		task.State = "needs_review"
		task.LastResult = "原进程记录不可核验，不重复执行"
		_, err := m.agent.updateSnapshot(task)
		return nil, err
	}
	if process.State == "running" {
		task.Next = now.Add(5 * time.Second)
		_, err := m.agent.updateSnapshot(task)
		return nil, err
	}
	return m.claimTaskResult(task, now, process, "读取原进程结果，自行决定检查文件、修复、运行验证、保存经验或回复。interrupted表示应核查，不自动重放。")
}
