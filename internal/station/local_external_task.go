package station

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/plugins"
	"wechat-bot/internal/strictjson"
)

const (
	localExternalTaskList    = "station_external_task_catalog"
	localExternalTaskCall    = "station_external_task"
	localExternalJobCap      = "external.job_request"
	localExternalPollCap     = "external.poll_request"
	localExternalTaskPurpose = "workstation external task"
)

var errLocalExternalTaskAuthority = errors.New("外部任务的原消息、连接或授权已变化")

func (t *localToolSet) localExternalTaskTools(ctx context.Context) []mcptools.Tool {
	env, err := t.externalTaskEnvironment(ctx)
	if err != nil || len(env.bridge.TaskTools()) == 0 {
		return nil
	}
	return []mcptools.Tool{
		mcptools.NewTool(localExternalTaskList, "分页查看已登记的异步 Job 和 Poll 工具及输入参数。", `{"type":"object","properties":{"query":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`),
		mcptools.NewTool(localExternalTaskCall, "调用异步工具：start 提交目录中的工具和参数；status 按 task_id 查结果并用 next_text_offset 续读；cancel 停止原任务。自动续办用 local_task_followup 关联 task_id，result_tool=station_external_task。", `{"type":"object","properties":{"action":{"type":"string","enum":["start","status","cancel"]},"tool":{"type":"string"},"arguments":{"type":"object"},"task_id":{"type":"string"},"text_offset":{"type":"integer","minimum":0},"text_limit":{"type":"integer","minimum":1}},"required":["action"],"additionalProperties":false}`),
	}
}

type localExternalTaskArgs struct {
	Action string          `json:"action"`
	Tool   string          `json:"tool"`
	Args   json.RawMessage `json:"arguments"`
	ID     string          `json:"task_id"`
	Offset int             `json:"text_offset"`
	Limit  int             `json:"text_limit"`
}

type localExternalTaskEnvironment struct {
	binding     localExternalBinding
	connections []plugins.Installed
	bridge      *plugins.ExternalBridge
	in          companion.Input
}

func (t *localToolSet) externalTaskEnvironment(ctx context.Context) (localExternalTaskEnvironment, error) {
	var env localExternalTaskEnvironment
	if t.run == nil || !t.run.Tracked || t.run.CheckSource(ctx) != nil {
		return env, errLocalExternalTaskAuthority
	}
	host := t.projectHostForDiscovery()
	if host == nil {
		return env, errLocalExternalTaskAuthority
	}
	grant, err := t.access.resolve()
	if err != nil {
		return env, errLocalExternalTaskAuthority
	}
	env.in = companion.Input{Channel: "local", Account: grant.FishID, Thread: grant.OwnerID, Sender: grant.OwnerID, TraceID: t.traceID}
	source, err := t.store.LocalRequestSource(ctx, env.in, t.traceID)
	if err != nil {
		return env, errLocalExternalTaskAuthority
	}
	installed, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localExternalPackage})
	if err != nil || !slices.Contains(installed.Permissions, "network") {
		return env, errLocalExternalTaskAuthority
	}
	config, err := host.store.Configuration(ctx, localExternalPackage)
	if err != nil {
		return env, err
	}
	raw, err := t.projectTools.externalRegistration()
	if err != nil {
		return env, errLocalExternalTaskAuthority
	}
	env.connections, env.bridge, err = externalBridgeRegistration(raw)
	if err != nil {
		return env, err
	}
	env.binding = localExternalBinding{tools: t, host: host, installed: installed, configRev: config.Revision,
		grant: grant, source: source, registration: externalDigest(raw)}
	return env, nil
}

type localExternalTask struct {
	env    localExternalTaskEnvironment
	def    plugins.ExternalTaskDefinition
	origin *companion.SourceRef
	trace  string
	args   json.RawMessage
	root   pluginhost.Job
}

func (t *localToolSet) callLocalExternalTask(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	env, err := t.externalTaskEnvironment(ctx)
	if err != nil {
		return "", err
	}
	if name == localExternalTaskList {
		return listLocalExternalHTTP(raw, env.bridge.TaskTools())
	}
	var input localExternalTaskArgs
	if len(raw) > 64<<10 {
		return "", mcptools.ArgumentError("$", "外部任务参数超过当前工作站的请求大小限制")
	}
	if _, err := strictjson.Decode(raw); err != nil || strictjson.DecodeKnownFields(raw, &input) != nil || input.Offset < 0 || input.Limit < 0 {
		return "", mcptools.ArgumentError("$", "请提供有效的外部任务参数与正文分页")
	}
	if input.Limit == 0 {
		input.Limit = 1200
	}
	var task localExternalTask
	if input.Action == "start" {
		if input.ID != "" || input.Offset != 0 {
			return "", mcptools.ArgumentError("$", "提交只接受目录中的工具和输入参数")
		}
		definition, err := env.bridge.Task(input.Tool)
		if err != nil {
			return "", mcptools.ArgumentError("$.tool", "该异步工具未在工作站启用")
		}
		args, err := strictjson.Decode(input.Args)
		object, ok := args.(map[string]any)
		if err != nil || !ok {
			return "", mcptools.ArgumentError("$.arguments", "请提供该工具声明的参数对象")
		}
		canonical, err := json.Marshal(object)
		if err != nil {
			return "", err
		}
		task = localExternalTask{env: env, def: definition, origin: env.binding.source, trace: t.traceID, args: canonical}
		task.root, _, err = task.invokePhase(ctx, "start", 0, task.initialTicket(), nil)
		if err != nil {
			return "", err
		}
	} else {
		if !validLocalRVCHex(input.ID, 64) || input.Tool != "" || input.Args != nil ||
			(input.Action != "status" && (input.Action != "cancel" || input.Offset != 0)) {
			return "", mcptools.ArgumentError("$", "查询和取消请使用原任务编号")
		}
		task, err = env.openTask(ctx, input.ID)
		if err != nil {
			return "", err
		}
	}
	refresh := input.Action == "status" && !t.inspectOnly && input.Offset == 0
	view, err := task.status(ctx, input, refresh)
	if err != nil {
		return "", err
	}
	return localProcessJSON(view)
}

func (e localExternalTaskEnvironment) openTask(ctx context.Context, id string) (localExternalTask, error) {
	var denied localExternalTask
	if !validLocalRVCHex(id, 64) {
		return denied, errLocalExternalTaskAuthority
	}
	job, err := e.binding.host.store.Job(ctx, id)
	var request localExternalTaskPhase
	if err != nil || job.Context.Purpose != localExternalTaskPurpose || len(job.Context.SourceVersions) != 1 ||
		strictjson.DecodeKnownFields(job.Arguments, &request) != nil || request.Phase != "start" {
		return denied, errLocalExternalTaskAuthority
	}
	def, err := e.bridge.Task(request.Tool)
	if err != nil {
		return denied, errLocalExternalTaskAuthority
	}
	scope := companion.ScopeID("local", e.in.Account, e.in.Thread)
	for eventID := range job.Context.SourceVersions {
		event, err := e.binding.tools.store.ReadEvent(ctx, []string{scope}, eventID)
		if err != nil || event.Role != "user" || event.Sender != e.in.Sender {
			return denied, errLocalExternalTaskAuthority
		}
		source, err := e.binding.tools.store.LocalRequestSource(ctx, e.in, event.Trace)
		if err != nil || source.EventID != eventID {
			return denied, errLocalExternalTaskAuthority
		}
		task := localExternalTask{env: e, def: def, origin: source, trace: event.Trace, args: request.Arguments, root: job}
		p := task.phase("start", 0, task.initialTicket(), nil)
		if err = p.checkStored(ctx, job); err != nil {
			return denied, errLocalExternalTaskAuthority
		}
		return task, nil
	}
	return denied, errLocalExternalTaskAuthority
}

func (t *localExternalTask) initialTicket() plugins.JobTicket {
	tool := t.def.Protocol.Tool
	return plugins.JobTicket{Tool: tool.Name, Fingerprint: tool.Fingerprint, State: "submitting"}
}

func (t *localExternalTask) lookup(ctx context.Context, phase string, ordinal int) (pluginhost.Job, error) {
	return t.env.binding.host.store.JobByRequest(ctx, localExternalPackage, t.env.in.Thread, t.phaseKey(phase, ordinal))
}

func (t *localExternalTask) latestPoll(ctx context.Context, phase string) (pluginhost.Job, int, error) {
	// A later ordinal is created only from the preceding completed waiting
	// phase. Thus a search of the durable keys cannot skip an uncertain phase.
	latest, ordinal := t.root, 0
	for lower, upper := 1, t.def.AttemptMax; lower <= upper; {
		mid := lower + (upper-lower)/2
		job, err := t.lookup(ctx, phase, mid)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			upper = mid - 1
		case err != nil:
			return latest, ordinal, err
		default:
			latest, ordinal = job, mid
			lower = mid + 1
		}
	}
	return latest, ordinal, nil
}

func (t *localExternalTask) status(ctx context.Context, input localExternalTaskArgs, refresh bool) (map[string]any, error) {
	view := map[string]any{"tool": t.def.Protocol.Tool.Name, "task_id": t.root.ID, "state": "needs_review"}
	host := t.env.binding.host.store
	pollPhase := "poll"
	if t.def.Protocol.Tool.Job == nil {
		pollPhase = "query"
	}
	rootPolicy := t.phase("start", 0, t.initialTicket(), nil)
	if err := rootPolicy.checkStored(ctx, t.root); err != nil {
		return nil, err
	}
	if t.root.State != "completed" {
		view["instruction"] = "原提交没有已核验结果，请核对原服务。"
		return view, nil
	}
	seed, _, err := rootPolicy.saved(ctx, t.root)
	if err != nil {
		return nil, err
	}
	stopKey := "station-task-stop:" + t.root.ID
	stop, err := host.ReadHostState(ctx, localExternalPackage, stopKey)
	if err != nil {
		return nil, err
	}
	if stop.Revision != 0 {
		if stop.SourceHash != t.root.Context.Authorization || string(stop.Records["stopped"]) != "true" {
			return nil, errLocalExternalTaskAuthority
		}
		view["state"], view["cancel_scope"] = "cancelled", "local_polling"
		return view, nil
	}
	cancelJob, err := t.lookup(ctx, "cancel", 0)
	if err == nil {
		policy := t.phase("cancel", 0, seed.Ticket, nil)
		if err = policy.checkStored(ctx, cancelJob); err != nil {
			return nil, err
		}
		view["cancel_scope"] = "remote_request"
		if cancelJob.State == "completed" {
			saved, _, err := policy.saved(ctx, cancelJob)
			if err != nil || !saved.CancelAck {
				return nil, errors.New("远程取消回执不可核验")
			}
			view["state"] = "cancel_acknowledged"
		}
		return view, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	latest, ordinal, err := t.latestPoll(ctx, pollPhase)
	if err != nil {
		return nil, err
	}
	view["attempts"], view["attempt_limit"] = ordinal, t.def.AttemptMax
	policy := rootPolicy
	if ordinal != 0 {
		policy = t.phase(pollPhase, ordinal, seed.Ticket, seed.Initial)
	}
	saved, output, err := policy.saved(ctx, latest)
	if err != nil {
		return nil, err
	}
	if latest.State == "completed" {
		view["state"] = saved.Ticket.State
		if saved.Ticket.State == "completed" || saved.Ticket.State == "failed" {
			return t.taskPage(view, output, input)
		}
	}
	if input.Action == "cancel" {
		if t.env.binding.tools.inspectOnly {
			return nil, errors.New("原请求中断后的恢复仅可读取已核验结果，请在新请求中取消")
		}
		if t.def.CanCancel && plugins.ValidExternalRemoteID(seed.Ticket.RemoteID) {
			cancelJob, _, err := t.invokePhase(ctx, "cancel", 0, seed.Ticket, nil)
			if err != nil {
				return nil, err
			}
			view["state"], view["cancel_scope"] = "needs_review", "remote_request"
			if cancelJob.State == "completed" {
				view["state"] = "cancel_acknowledged"
			}
			return view, nil
		}
		if err := rootPolicy.checkStored(ctx, t.root); err != nil {
			return nil, err
		}
		if _, err := host.InitializeHostState(ctx, localExternalPackage, stopKey, t.root.Context.Authorization,
			map[string]json.RawMessage{"stopped": json.RawMessage("true")}); err != nil {
			return nil, err
		}
		view["state"], view["cancel_scope"] = "cancelled", "local_polling"
		return view, nil
	}
	if latest.State != "completed" {
		view["instruction"] = "最近一次查询结果待核对，未自动重发。"
		return view, nil
	}
	started, err := time.Parse(time.RFC3339Nano, t.root.Created)
	updated, updateErr := time.Parse(time.RFC3339Nano, latest.Updated)
	if err != nil || updateErr != nil {
		return nil, errors.New("外部任务时间记录不可核验")
	}
	deadline := started.Add(time.Duration(t.def.Deadline) * time.Second)
	due := updated.Add(time.Duration(t.def.Interval) * time.Second)
	now := time.Now().UTC()
	view["deadline"], view["next_check_at"] = deadline, minTime(due, deadline)
	if !now.Before(deadline) || ordinal >= t.def.AttemptMax {
		view["state"] = "polling_limit_reached"
		delete(view, "next_check_at")
		return view, nil
	}
	if refresh && !now.Before(due) {
		pollCtx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		job, payload, err := t.invokePhase(pollCtx, pollPhase, ordinal+1, seed.Ticket, seed.Initial)
		if err != nil {
			return nil, err
		}
		view["attempts"] = ordinal + 1
		if job.State != "completed" {
			view["state"] = "needs_review"
			return view, nil
		}
		view["state"] = payload.Ticket.State
		updated, err = time.Parse(time.RFC3339Nano, job.Updated)
		if err != nil {
			return nil, err
		}
		view["next_check_at"] = minTime(updated.Add(time.Duration(t.def.Interval)*time.Second), deadline)
		if payload.Ticket.State == "completed" {
			_, output, err := t.phase(pollPhase, ordinal+1, seed.Ticket, seed.Initial).saved(ctx, job)
			if err != nil {
				return nil, err
			}
			return t.taskPage(view, output, input)
		}
		if payload.Ticket.State == "waiting" && ordinal+1 >= t.def.AttemptMax {
			view["state"] = "polling_limit_reached"
		}
		if view["state"] != "waiting" {
			delete(view, "next_check_at")
		}
	}
	return view, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (t *localExternalTask) taskPage(view map[string]any, result json.RawMessage, input localExternalTaskArgs) (map[string]any, error) {
	if view["state"] != "completed" {
		return view, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(result, &fields) != nil {
		return nil, errors.New("外部任务成品未核验")
	}
	public := map[string]json.RawMessage{}
	for _, key := range []string{"text", "media", "artifact", "artifacts", "content_type"} {
		if value, ok := fields[key]; ok {
			public[key] = value
		}
	}
	raw, err := json.Marshal(public)
	if err != nil {
		return nil, err
	}
	page, err := localExternalTextPage(raw, input.Offset, input.Limit)
	if err != nil {
		return nil, err
	}
	view["output"], view["untrusted_external_content"] = page, true
	delete(view, "next_check_at")
	return view, nil
}

func (t *localToolSet) externalFollowupTask(ctx context.Context, id string) (localExternalTask, error) {
	env, err := t.externalTaskEnvironment(ctx)
	if err != nil {
		return localExternalTask{}, errLocalExternalTaskAuthority
	}
	task, err := env.openTask(ctx, id)
	if err != nil || task.trace != t.traceID || !slices.ContainsFunc(t.run.Operations, func(op companion.LocalToolCheckpoint) bool {
		return op.Tool == localExternalTaskCall && op.Key == task.root.Context.RequestKey && op.Grant == task.root.Context.Authorization
	}) {
		return localExternalTask{}, errLocalExternalTaskAuthority
	}
	return task, nil
}

func (t *localToolSet) checkFollowupTarget(ctx context.Context, tool, id string) error {
	if tool == localExternalTaskCall {
		task, err := t.externalFollowupTask(ctx, id)
		if err != nil {
			return err
		}
		view, err := task.status(ctx, localExternalTaskArgs{Limit: 1}, false)
		if err != nil || view["state"] == "cancelled" || view["state"] == "cancel_acknowledged" || view["cancel_scope"] != nil {
			return errLocalExternalTaskAuthority
		}
		return nil
	}
	project, err := t.projectFollowupJob(ctx, tool, id)
	if err != nil || project.CancelRequested || project.State == "cancelled" {
		return errProjectFollowupAuthority
	}
	return nil
}

func (t *localToolSet) readExternalTaskFollowup(ctx context.Context, job companion.LocalTaskFollowup, result companion.RecoveryToolResult) (companion.RecoveryToolResult, string, string, time.Duration) {
	task, err := t.externalFollowupTask(ctx, job.JobID)
	if err != nil {
		return result, "cancelled", "external_task_authorization_changed", 0
	}
	view, err := task.status(ctx, localExternalTaskArgs{Limit: 1200}, true)
	if errors.Is(err, errLocalExternalTaskAuthority) {
		return result, "cancelled", "external_task_authorization_changed", 0
	}
	if err != nil {
		return result, "waiting", "external_task_status_retry", 2 * time.Second
	}
	if view["state"] == "waiting" {
		next, _ := view["next_check_at"].(time.Time)
		return result, "waiting", "waiting_for_external_task", max(time.Second, time.Until(next))
	}
	if view["state"] == "cancelled" || view["state"] == "cancel_acknowledged" || view["cancel_scope"] != nil {
		return result, "cancelled", "external_task_cancelled", 0
	}
	raw, err := localProcessJSON(view)
	if err != nil {
		return result, "failed", "external_task_result_unavailable", 0
	}
	result.Tool, result.ResultID, result.Result = localExternalTaskCall, job.JobID, raw
	result.OccurredAt = time.Now().UTC().Format(time.RFC3339Nano)
	return result, "ready", "", 0
}
