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
	return p.callProjectPlugin(ctx, "server_process", raw)
}
func (p *permittedTools) callProcessPlatform(ctx context.Context, raw json.RawMessage, windows bool) (string, error) {
	name := "server_process"
	if windows {
		name = "server_windows_process"
	}
	return p.callProjectPlugin(ctx, name, raw)
}

// The host retains durable process identity, sandbox admission, cancellation
// and original actor follow-up. This is reached only through the bound broker.
func (p *permittedTools) mediateWorkspaceProcess(ctx context.Context, raw json.RawMessage, windows bool, authorized func() bool) (result string, callErr error) {
	defer func() { callErr = localAgentError(callErr) }()
	var in struct {
		GPU                       bool `json:"gpu"`
		FishHome                  bool `json:"fish_home"`
		Action, ID, Path, Command string
		Argv                      []string `json:"argv"`
		Key                       string   `json:"request_key"`
		Seconds                   int      `json:"timeout_seconds"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return "", errors.New("进程参数无效")
	}
	scope := p.processScope()
	// Fish-home jobs live in a deliberately separate namespace.  A normal
	// conversation scope must never see them, and the durable fish ID below is
	// only a selector for this call -- boundFishHome rechecks the live owner,
	// direct-origin and binding state on every operation.
	fishHomeID := ""
	if in.FishHome {
		_, currentFishHomeID, fishHomeErr := p.boundFishHome()
		if fishHomeErr != nil {
			return "", fishHomeErr
		}
		fishHomeID = currentFishHomeID
		scope = agentID(p.trace.Account, p.trace.Thread, p.trace.Sender, "fish_home", fishHomeID)
	}
	cfg, configErr := p.scopedHostConfig(raw)
	if configErr != nil {
		return "", &RuntimeFailure{Code: "execution_scope_unavailable", Stage: "scope", Message: "程序未启动：当前请求的执行范围不可用", NotStarted: true, EffectiveScope: scope, Continuation: "核对当前账号、会话及global授权后再调用server_status"}
	}
	cfg.Isolation.GPU = in.GPU
	if !p.workspaceExecutionAllowed() || cfg.Mode != "full" {
		return "", &RuntimeFailure{Code: "execution_permission_denied", Stage: "authorization", Message: "后台程序执行未授权", NotStarted: true, EffectiveScope: scope, Continuation: "核对当前真实请求者和执行权限；不得重用其他账号或会话的授权"}
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
			current, e := access.scopedHostConfig(raw)
			if e != nil || !access.workspaceExecutionAllowed() || current.Mode != "full" || !slices.Equal(current.Roots, cfg.Roots) {
				return false
			}
			if fishHomeID != "" {
				_, currentFishID, fishErr := access.boundFishHome()
				if fishErr != nil || currentFishID != fishHomeID {
					return false
				}
			}
			return authorized != nil && authorized()
		}
		toolName := "server_process"
		if windows {
			toolName = "server_windows_process"
		}
		run := func() (any, error) {
			var value any
			var startErr error
			if windows {
				if len(in.Argv) > 0 {
					value, startErr = p.manager.processes.StartWindowsArgv(cfg, scope, in.Key, in.Path, in.Argv, in.Seconds, allowed)
				} else {
					value, startErr = p.manager.processes.StartWindows(cfg, scope, in.Key, in.Path, in.Command, in.Seconds, allowed)
				}
			} else {
				value, startErr = p.manager.processes.Start(cfg, scope, in.Key, in.Path, in.Command, in.Seconds, allowed)
			}
			if startErr != nil {
				return nil, processAdmissionFailure(startErr.Error(), scope)
			}
			return value, nil
		}
		var result string
		var err error
		if p.privileged() {
			result, err = p.privilegedCall(ctx, toolName, raw, run)
		} else {
			v, e := run()
			err = e
			b, _ := json.Marshal(v)
			result = string(b)
		}
		if err != nil {
			return "", err
		}
		var process struct {
			ID     string `json:"id"`
			State  string `json:"state"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal([]byte(result), &process) == nil && process.State == "rejected" {
			return "", processAdmissionFailure(process.Reason, scope)
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
			task := agentTask{OwnerRequested: p.privileged(), ParentID: p.trace.TaskID, ID: taskID, ProcessID: process.ID, FishHomeID: fishHomeID, Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, Title: "后台程序", RequestedText: request, Checkpoint: "等待真实程序结果，使用原进程编号查询，不重新运行命令", State: "waiting", Next: time.Now().Add(2 * time.Second), OriginTrace: origin, QuoteServerID: p.trace.QuoteServerID}
			task.Constraints = p.taskConstraints("process", "", "", "")
			if task.ParentID != "" {
				task.RequestedText = ""
				task.Goal = "读取该程序真实结果并通知父任务；不是重新执行原始用户请求"
			}
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
	scope := p.processScope()
	if task.FishHomeID != "" {
		// A persisted fish ID is deliberately insufficient. A restart may happen
		// after unbinding, after owner revocation, or after a task was forged from
		// stale state. Re-resolve against the current trusted direct owner before
		// touching the process registry, and never try the old conversation scope.
		_, currentFishID, fishErr := p.boundFishHome()
		if fishErr != nil || currentFishID != task.FishHomeID {
			task.State = "cancelled"
			task.LastResult = "鱼屋绑定或主人私聊授权已变化，不读取旧进程内容"
			_, err := m.agent.updateSnapshot(task)
			return nil, err
		}
		scope = agentID(task.Account, task.Thread, task.Sender, "fish_home", task.FishHomeID)
	}
	if !p.workspaceExecutionAllowed() || task.OwnerRequested && !p.privileged() || m.agent.config().Host.Mode != "full" {
		task.State = "cancelled"
		task.LastResult = "执行权限已撤销，不交付旧进程内容"
		_, err := m.agent.updateSnapshot(task)
		return nil, err
	}
	process, err := m.processes.Status(scope, task.ProcessID, false)
	// Pre-fish-home tasks retain their established compatibility fallback. It
	// must never apply to a fish-home watcher because that would let a revoked
	// binding inspect an old process namespace.
	if err != nil && task.FishHomeID == "" && p.privileged() {
		process, err = m.processes.Status(agentID(task.Account, task.Thread, task.Sender), task.ProcessID, false)
	}
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
	if task.ParentID != "" {
		a := m.agent
		a.mu.Lock()
		parent, ok := a.data.Tasks[task.ParentID]
		child, current := a.data.Tasks[task.ID]
		if current && child.Updated.Equal(task.Updated) && ok && parent.Account == task.Account && parent.Thread == task.Thread && parent.Sender == task.Sender {
			oldParent := copyTask(parent)
			result, _ := json.Marshal(process)
			parent.Actions = append(append([]taskAction{}, parent.Actions...), taskAction{Tool: "background_process_result", State: process.State, At: now, Result: logText(string(result), 2200)})
			if parent.State == "waiting" || parent.State == "pending" {
				parent.Next = now
			}
			parent.Updated = now
			child.State = "completed"
			child.WorkCompleted = true
			child.LastResult = "程序结果已并入父任务，不单独重复回复原请求"
			child.Updated = now
			a.data.Tasks[parent.ID] = parent
			a.data.Tasks[child.ID] = child
			err := a.persist()
			if err != nil {
				a.data.Tasks[parent.ID] = oldParent
				a.data.Tasks[child.ID] = task
			}
			a.mu.Unlock()
			if err == nil {
				err = a.retireSettled()
			}
			return nil, err
		}
		if _, retired := a.archived[task.ParentID]; retired && current {
			child.State = "completed"
			child.WorkCompleted = true
			child.LastResult = "父任务已结束，程序结果保留，不再单独续办原请求"
			a.data.Tasks[child.ID] = child
			err := a.persist()
			a.mu.Unlock()
			if err == nil {
				err = a.retireSettled()
			}
			return nil, err
		}
		a.mu.Unlock()
	}
	return m.claimTaskResult(task, now, process, "读取原进程结果，自行决定检查文件、修复、运行验证、保存经验或回复。interrupted表示应核查，不自动重放。")
}

func (p *permittedTools) processScope() string {
	if p.privileged() {
		return agentID(p.trace.Account, p.trace.Thread, p.trace.Sender)
	}
	return agentID(p.trace.Account, p.trace.Thread, p.trace.Sender, "conversation")
}
