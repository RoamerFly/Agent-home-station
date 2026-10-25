package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/plugins"
)

func (a *agentControl) pluginUpdate(t agentTask) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	current, ok := a.data.Tasks[t.ID]
	if !ok || !current.Updated.Equal(t.Updated) || current.State == "cancelled" {
		return false, nil
	}
	// Copy maps/pointers so a cancelled task cannot be resurrected by an old
	// poll response, and failed persistence never mutates the previous value.
	t.Updated = time.Now().UTC()
	a.data.Tasks[t.ID] = t
	if err := a.persist(); err != nil {
		a.data.Tasks[t.ID] = current
		return false, err
	}
	return true, nil
}
func copyTask(t agentTask) agentTask {
	b, _ := json.Marshal(t)
	var out agentTask
	_ = json.Unmarshal(b, &out)
	return out
}
func (m *Manager) agentTaskActive(in companion.Input) bool {
	if in.TaskID == "" {
		return true
	}
	for _, t := range m.agent.list(in.Account, in.Thread) {
		if t.ID == in.TaskID {
			return t.State != "cancelled"
		}
	}
	return false
}
func (m *Manager) pluginRuntime() (*plugins.Runtime, error) {
	return plugins.New(m.plugins.installed())
}
func (p *permittedTools) pluginTaskAllowed() bool {
	a := p.manager.agent.config()
	return a.Enabled && a.TasksEnabled && !p.trace.Preview && !p.trace.Surf && p.trace.Account == p.manager.native.Account() && p.trace.Thread != "" && permittedRecipient(p.manager.Config().Config, p.trace.Thread)
}
func (p *permittedTools) pluginTaskTools() []mcptools.Tool {
	if !p.pluginTaskAllowed() {
		return nil
	}
	return []mcptools.Tool{
		agentTool("plugin_job_status", "Read or prepare an existing background plugin job in THIS conversation; does not create a new job. Use task_id returned by the original plugin. Completed output may be staged into media IDs for final reply. Input {task_id:string}.", `{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"],"additionalProperties":false}`),
		agentTool("plugin_job_cancel", "Stop this requester's existing background plugin job and future delivery. Calls the owner-configured remote cancel endpoint once when supported. An uncertain cancel is never automatically replayed. Input {task_id:string}.", `{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"],"additionalProperties":false}`),
	}
}
func pluginJobSummary(t agentTask) map[string]any {
	return map[string]any{"task_id": t.ID, "job_id": t.PluginJob.Ticket.RemoteID, "state": t.State, "remote_state": t.PluginJob.Ticket.State, "automatic_followup": t.State == "waiting" || t.State == "pending", "cancel_state": t.PluginJob.CancelState, "instruction": "这是同一条已保存外部任务的状态，不代表微信已发送。等待完成后自动交付原会话，不重新创建；也可以查询plugin_job_status或取消plugin_job_cancel。不要向群友展示内部编号。"}
}
func (p *permittedTools) startPluginJob(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if !p.pluginTaskAllowed() {
		return "", errors.New("后台插件任务需要启用持续任务并选择已授权会话；预演不创建远端任务")
	}
	var arg map[string]any
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if json.Unmarshal(args, &arg) != nil || arg == nil || len(args) > 8192 {
		return "", errors.New("插件参数无效")
	}
	canonical, _ := json.Marshal(arg)
	if err := p.runtime.CheckJobArgs(name, canonical); err != nil {
		return "", err
	}
	id := "plugin-" + agentID(p.trace.Account, p.trace.Thread, p.trace.TraceID, name)[:48]
	for _, old := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
		if old.PluginJob != nil && (old.ID == id || p.trace.TaskID == old.ID) {
			b, _ := json.Marshal(pluginJobSummary(old))
			return string(b), nil
		}
	}
	ticket, err := p.runtime.NewJobTicket(name)
	if err != nil {
		return "", err
	}
	interval, deadline, polls := p.runtime.JobLimits(name)
	t := agentTask{ID: id, Account: p.trace.Account, Thread: p.trace.Thread, Sender: p.trace.Sender, Title: "插件任务 · " + name, OriginTrace: p.trace.TraceID, QuoteServerID: p.trace.QuoteServerID, RequestedText: logText(companion.UserRequestText(p.trace.Text), 2000), State: "waiting", Next: time.Now().Add(time.Duration(interval) * time.Second), PluginJob: &pluginJobState{Ticket: ticket, Deadline: time.Now().Add(time.Duration(deadline) * time.Second), IntervalSeconds: interval, MaxPolls: polls}}
	// Claim the task before the POST. The claim itself is scoped and atomic.
	a := p.manager.agent
	a.mu.Lock()
	if _, exists := a.data.Tasks[id]; exists {
		old := a.data.Tasks[id]
		a.mu.Unlock()
		b, _ := json.Marshal(pluginJobSummary(old))
		return string(b), nil
	}
	if len(a.data.Tasks) >= 500 {
		a.mu.Unlock()
		return "", errors.New("任务记录达到上限")
	}
	t.Updated = time.Now().UTC()
	a.data.Tasks[id] = t
	if err = a.persist(); err != nil {
		delete(a.data.Tasks, id)
		a.mu.Unlock()
		return "", err
	}
	a.mu.Unlock()
	// The in-memory ledger must own a distinct copy from the network caller.
	t = copyTask(t)
	created, callErr := p.runtime.StartJob(ctx, ticket, canonical)
	if callErr != nil && created.RemoteID != "" {
		created.State = "waiting"
		callErr = nil
	}
	t.PluginJob.Ticket = created
	if callErr != nil {
		t.State = "needs_review"
		t.LastResult = "插件创建结果无法确认，未自动重试；请核对服务商记录"
	} else {
		t.LastResult = "外部任务已领取，后台查询同一任务，不重新创建"
	}
	saved, saveErr := a.pluginUpdate(t)
	if saveErr != nil {
		return "", saveErr
	}
	if !saved {
		for _, old := range a.list(t.Account, t.Thread) {
			if old.ID == t.ID {
				b, _ := json.Marshal(pluginJobSummary(old))
				return string(b), nil
			}
		}
		return "", errors.New("插件任务状态已改变")
	}
	p.manager.record(bot.Record{Event: "plugin_job_submitted", Category: "tool", ThreadID: t.Thread, MessageID: t.OriginTrace, Tool: name, Status: t.State, Reason: "plugin_job_durable_claim", Content: t.LastResult, Origin: "agent"})
	if callErr != nil {
		return "", errors.New("plugin_job_submission_unknown")
	}
	b, _ := json.Marshal(pluginJobSummary(t))
	return string(b), nil
}
func (p *permittedTools) callPluginJob(ctx context.Context, name string, args json.RawMessage) (string, error) {
	var in struct {
		TaskID string `json:"task_id"`
	}
	if json.Unmarshal(args, &in) != nil || len(in.TaskID) > 128 {
		return "", errors.New("插件任务参数无效")
	}
	var task *agentTask
	for _, old := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
		if old.ID == in.TaskID && old.PluginJob != nil {
			copy := copyTask(old)
			task = &copy
		}
	}
	if task == nil || task.Sender != p.trace.Sender && !p.privileged() {
		return "", errors.New("只能查询或取消本人在本会话发起的插件任务")
	}
	if name == "plugin_job_cancel" {
		out, err := p.manager.cancelPluginTask(ctx, *task)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(pluginJobSummary(out))
		return string(b), nil
	}
	if task.State == "cancelled" || task.State == "completed" || task.PluginJob.Ticket.State != "completed" {
		b, _ := json.Marshal(pluginJobSummary(*task))
		return string(b), nil
	}
	if p.trace.TaskID != task.ID || task.State != "running" {
		b, _ := json.Marshal(pluginJobSummary(*task))
		return string(b), nil
	}
	if task.Delivery != nil || task.PluginJob.Prepared {
		return "", errors.New("任务已进入微信交付，请核对原回执，不重复暂存或发送")
	}
	if !p.enabled(task.PluginJob.Ticket.Tool) {
		return "", errors.New("原插件或媒体权限已关闭")
	}
	if _, err := p.runtime.NewJobTicket(task.PluginJob.Ticket.Tool); err != nil {
		return "", err
	}
	// Validate the original manifest fingerprint before accessing cached output.
	current, _ := p.runtime.NewJobTicket(task.PluginJob.Ticket.Tool)
	if current.Fingerprint != task.PluginJob.Ticket.Fingerprint {
		return "", errors.New("插件声明已改变，旧任务未交给新接口")
	}
	result, err := p.stageOutput(ctx, task.PluginJob.Ticket.Output)
	if err != nil {
		return "", err
	}
	task.PluginJob.Prepared = true
	if saved, err := p.manager.agent.pluginUpdate(*task); err != nil {
		return "", err
	} else if !saved {
		return "", errors.New("任务状态已改变，未交付过期结果")
	}
	var staged struct {
		Media []companion.GeneratedMedia `json:"media"`
	}
	if json.Unmarshal([]byte(result), &staged) == nil && len(staged.Media) > 0 {
		var body map[string]any
		_ = json.Unmarshal([]byte(result), &body)
		ids := []string{}
		for _, item := range staged.Media {
			ids = append(ids, item.ID)
		}
		body["required_media_ids"] = ids
		b, _ := json.Marshal(body)
		result = string(b)
	}
	return result, nil
}
func (m *Manager) claimPluginTask(task agentTask, now time.Time) (*companion.Input, error) {
	task = copyTask(task)
	job := task.PluginJob
	if job.Ticket.State == "submitting" {
		task.State = "needs_review"
		task.LastResult = "创建结果未知，不自动重新创建"
		_, err := m.agent.pluginUpdate(task)
		return nil, err
	}
	runtime, err := m.pluginRuntime()
	if err != nil {
		return nil, err
	}
	p, _, err := m.toolAccess()
	if err != nil {
		return nil, err
	}
	defer p.hub.Close()
	p.SetTrace(companion.Input{Channel: "wechat", Account: task.Account, Thread: task.Thread, Sender: task.Sender})
	if !p.enabled(job.Ticket.Tool) {
		task.LastResult = "插件或任务权限已关闭，保留任务，不创建替代任务"
		task.Next = now.Add(time.Minute)
		_, err := m.agent.pluginUpdate(task)
		return nil, err
	}
	if job.Ticket.State == "waiting" {
		if !now.Before(job.Deadline) || job.Polls >= job.MaxPolls {
			task.State = "needs_review"
			task.LastResult = "插件任务超过本地查询期限或次数，未自动重新生成；远端可能仍在运行"
			_, err := m.agent.pluginUpdate(task)
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		ticket, pollErr := runtime.PollJob(ctx, job.Ticket)
		cancel()
		job.Polls++
		if pollErr != nil {
			if errors.Is(pollErr, plugins.ErrJobConfigChanged) {
				task.State = "needs_review"
				task.LastResult = "插件声明已改变，旧任务未交给新接口；请核对原服务商任务"
				_, err := m.agent.pluginUpdate(task)
				return nil, err
			}
			task.LastResult = "插件任务状态暂时不可读，稍后查询同一任务，不重新创建"
			task.Next = now.Add(time.Duration(job.IntervalSeconds) * time.Second)
			_, err := m.agent.pluginUpdate(task)
			return nil, err
		}
		job.Ticket = ticket
		if ticket.State == "waiting" {
			task.LastResult = "外部任务排队或执行中，尚未取得完成结果"
			task.Next = now.Add(time.Duration(job.IntervalSeconds) * time.Second)
			_, err := m.agent.pluginUpdate(task)
			return nil, err
		}
	}
	if task.Runs >= 3 {
		task.State = "needs_review"
		task.LastResult = "插件结果多次未交付，请检查原任务；不会重新创建"
		_, err := m.agent.pluginUpdate(task)
		return nil, err
	}
	task.State = "running"
	task.Runs++
	task.LastResult = "插件结果已就绪，准备交付原会话"
	if job.Ticket.State == "failed" {
		job.Failure = true
		job.Prepared = true
		task.LastResult = "外部插件任务失败，准备告知原会话"
	}
	if saved, err := m.agent.pluginUpdate(task); err != nil || !saved {
		return nil, err
	}
	in := &companion.Input{Channel: "wechat", Account: task.Account, Thread: task.Thread, Sender: task.Sender, Mode: "direct", IsGroup: strings.HasSuffix(task.Thread, "@chatroom"), Directed: true, TaskID: task.ID, OriginTraceID: task.OriginTrace, RequestedText: task.RequestedText, TraceID: "plugin-result-" + task.ID + "-" + now.UTC().Format("150405"), QuoteServerID: task.QuoteServerID}
	if job.Failure {
		in.Text = "[已请求插件任务的失败通知，不是新用户请求] 外部任务已报告失败。原需求资料：" + task.RequestedText + "。如实说明没有完成，不重新创建。"
	} else {
		in.Text = "[已请求插件任务的结果交付，不是新用户请求] plugin_task=" + task.ID + "；原需求资料：" + task.RequestedText + "。调用plugin_job_status(task_id=该编号)取得真实输出，结合最新会话判断是否取消或交付。附件用media_ids，介绍在附件接受后发送；文字输出自然回应，不暴露接口或任务编号。不重新调用创建工具。"
	}
	m.record(bot.Record{Event: "plugin_job_result_ready", Category: "tool", ThreadID: task.Thread, MessageID: task.OriginTrace, Tool: job.Ticket.Tool, Status: job.Ticket.State, Reason: "plugin_job_same_task_result", Origin: "agent"})
	return in, nil
}
func (m *Manager) cancelPluginTask(ctx context.Context, task agentTask) (agentTask, error) {
	if task.State == "cancelled" || task.State == "completed" {
		return task, nil
	}
	task = copyTask(task)
	task.State = "cancelled"
	task.LastResult = "已停止本地跟进和后续交付；正在核对远端取消能力"
	task.PluginJob.CancelState = "claimed"
	a := m.agent
	a.mu.Lock()
	current, ok := a.data.Tasks[task.ID]
	if !ok {
		a.mu.Unlock()
		return task, errors.New("任务不存在")
	}
	if current.State == "cancelled" {
		a.mu.Unlock()
		return current, nil
	}
	if !current.Updated.Equal(task.Updated) {
		a.mu.Unlock()
		return task, errors.New("任务状态已改变，请刷新")
	}
	task.Updated = time.Now().UTC()
	a.data.Tasks[task.ID] = copyTask(task)
	if err := a.persist(); err != nil {
		a.data.Tasks[task.ID] = current
		a.mu.Unlock()
		return task, err
	}
	a.mu.Unlock()
	runtime, err := m.pluginRuntime()
	if err == nil {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = runtime.CancelJob(ctx, task.PluginJob.Ticket)
		cancel()
	}
	task.PluginJob.CancelState = "requested"
	task.LastResult = "已停止本地交付，远端已接受取消请求；不代表远端已确认终止"
	if err != nil {
		task.PluginJob.CancelState = "unconfirmed"
		task.LastResult = "已停止本地交付；远端取消不支持、不可用或结果未知，未自动重试"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current = a.data.Tasks[task.ID]
	if current.State == "cancelled" && current.PluginJob.CancelState == "claimed" {
		a.data.Tasks[task.ID] = copyTask(task)
		if err := a.persist(); err != nil {
			a.data.Tasks[task.ID] = current
			return task, err
		}
	}
	return task, nil
}
