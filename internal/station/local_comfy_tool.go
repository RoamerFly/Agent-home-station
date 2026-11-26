package station

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"wechat-bot/internal/comfyui"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
)

const (
	localComfyPackage      = "comfyui"
	localComfyCapability   = "comfyui.run"
	localComfyListTool     = "project_comfy_workflows"
	localComfyModelsTool   = "project_comfy_models"
	localComfyFilesTool    = "project_comfy_files"
	localComfyRegisterTool = "project_comfy_register"
	localComfyStartTool    = "project_comfy_run"
	localComfyJobTool      = "project_comfy_job"
	localComfyImageLimit   = 16
)

var localComfyGrants = []string{"artifact.write", "gpu"}
var errLocalComfyWorkflowSetup = errors.New("请先登记并校验至少一个获准的 ComfyUI 工作流")

func (s *localProjectTools) comfySetup(ctx context.Context, fish string) (localComfyDeployment, []localComfyWorkflow, error) {
	if s.comfy == nil {
		return localComfyDeployment{}, nil, errors.New("本机 ComfyUI 后端未登记")
	}
	deployment, err := s.comfy.Info(ctx, fish)
	if err != nil || deployment.Binding == "" {
		return localComfyDeployment{}, nil, errors.New(localComputeSetupReason(localComfyPackage, err))
	}
	workflows, err := loadLocalComfyWorkflows(deployment.WorkflowRoot)
	if err != nil || len(workflows) == 0 {
		return deployment, nil, errLocalComfyWorkflowSetup
	}
	record, err := s.home.identity.Read()
	if err != nil || record.FishID != fish || record.LocalOwner == nil {
		return deployment, nil, errors.New("ComfyUI 家目录身份不可用")
	}
	home, err := s.home.directory(record)
	if err != nil {
		return deployment, nil, err
	}
	custom := filepath.Join(home, "workflows", "comfyui")
	if err := localComfyInstallStarters(custom); err != nil {
		return deployment, nil, err
	}
	if _, err := os.Stat(filepath.Join(custom, localComfyWorkflowRegistryFile)); err == nil {
		personal, err := loadLocalComfyWorkflows(custom)
		if err != nil {
			return deployment, nil, err
		}
		ids := map[string]bool{}
		for _, item := range workflows {
			ids[item.ID] = true
		}
		for _, item := range personal {
			if ids[item.ID] {
				return deployment, nil, errors.New("ComfyUI 自建工作流编号与已登记工作流重复")
			}
			workflows = append(workflows, item)
		}
	}
	return deployment, workflows, nil
}

func (t *localToolSet) localComfyTools(ctx context.Context) []mcptools.Tool {
	s := t.projectTools
	if s == nil || s.comfy == nil || t.run == nil || !t.run.Tracked {
		return nil
	}
	grant, err := t.access.resolve()
	if err != nil {
		return nil
	}
	host, err := s.forFish(grant.FishID)
	if err != nil {
		return nil
	}
	if _, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localComfyPackage, capability: localComfyCapability}); err != nil {
		return nil
	}
	jobTool := localComfyJobToolSpec()
	if _, _, err := s.comfySetup(ctx, grant.FishID); err != nil {
		return []mcptools.Tool{localComfyModelTool(), localComfyFilesToolSpec(), localComfyRegisterToolSpec(), jobTool}
	}
	return []mcptools.Tool{
		localComfyWorkflowTool(),
		localComfyModelTool(),
		localComfyFilesToolSpec(),
		localComfyRegisterToolSpec(),
		localComfyStartToolSpec(),
		jobTool,
	}
}

func localComfyWorkflowTool() mcptools.Tool {
	return mcptools.NewTool(localComfyListTool, "列出可用 ComfyUI 工作流及参数。新图写入 home/workflows/comfyui，再用 project_comfy_register 登记。", `{"type":"object","additionalProperties":false}`)
}

func localComfyModelTool() mcptools.Tool {
	return mcptools.NewTool(localComfyModelsTool, "按 category、offset、limit 浏览模型供创作选用。具体模型清单和路径仅向管理员回复；其他成员询问时介绍可提供的创作能力。", `{"type":"object","properties":{"category":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`)
}

func localComfyFilesToolSpec() mcptools.Tool {
	return mcptools.NewTool(localComfyFilesTool, "浏览或编辑 ComfyUI 工具、工作流、模型与工作区目录。root 选 runtime/models/model_library/custom_nodes/workflows/home；read 可分页，修改已有文件传 expected_sha256。新图用 project_comfy_register 登记。模型路径仅向管理员披露。", `{"type":"object","properties":{"action":{"type":"string","enum":["list","read","write"]},"root":{"type":"string","enum":["runtime","models","model_library","custom_nodes","workflows","home"]},"path":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1},"content":{"type":"string"},"expected_sha256":{"type":"string"}},"required":["action","root"],"additionalProperties":false}`)
}

func localComfyRegisterToolSpec() mcptools.Tool {
	return mcptools.NewTool(localComfyRegisterTool, "登记或更新工作流：先写 graph JSON，再传 id、label、graph、save_node、parameters；返回已校验的工作流视图。", `{"type":"object","properties":{"id":{"type":"string"},"label":{"type":"string"},"graph":{"type":"string"},"save_node":{"type":"string"},"parameters":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"label":{"type":"string"},"kind":{"type":"string","enum":["image","string","number","integer","boolean"]},"required":{"type":"boolean"},"minimum":{"type":"number"},"maximum":{"type":"number"},"max_length":{"type":"integer"},"enum":{"type":"array","items":{"type":"string"}},"node":{"type":"string"},"input":{"type":"string"}},"required":["id","label","kind","node","input"],"additionalProperties":false}}},"required":["id","label","graph","save_node","parameters"],"additionalProperties":false}`)
}

func localComfyStartToolSpec() mcptools.Tool {
	return mcptools.NewTool(localComfyStartTool, "启动已批准的本机 ComfyUI 工作流并取得任务编号；服务空闲关闭时会自动启动。inputs 只填工作流列表给出的参数。依据工作流的输出类型，从任务查询取得 PNG 或 MP4。", `{"type":"object","properties":{"workflow_id":{"type":"string"},"inputs":{"type":"object","additionalProperties":{"type":["string","integer","number","boolean"]}},"title":{"type":"string","maxLength":100}},"required":["workflow_id"],"additionalProperties":false}`)
}

func localComfyJobToolSpec() mcptools.Tool {
	return mcptools.NewTool(localComfyJobTool, "查询原 ComfyUI 任务或取消。使用先前返回的 job_id。查询取得成品后，在回复中保留作品编号，界面可供主人下载。", `{"type":"object","properties":{"job_id":{"type":"string"},"action":{"type":"string","enum":["status","cancel"]}},"required":["job_id","action"],"additionalProperties":false}`)
}

type localComfyInvocation struct {
	tool           *localToolSet
	host           *localProjectToolHost
	grant          localWorkspaceGrant
	installed      pluginhost.InstalledPackage
	deployment     localComfyDeployment
	workflows      []localComfyWorkflow
	configRevision string
}

func (t *localToolSet) callLocalComfy(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	s := t.projectTools
	if s == nil || s.comfy == nil || t.run == nil || !t.run.Tracked {
		return "", errors.New("当前对话没有本机 ComfyUI 执行授权")
	}
	if err := t.run.CheckSource(ctx); err != nil {
		return "", err
	}
	s.update.Lock()
	defer s.update.Unlock()
	grant, err := t.access.resolve()
	if err != nil {
		return "", err
	}
	host, err := s.forFish(grant.FishID)
	if err != nil {
		return "", err
	}
	installed, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localComfyPackage, capability: localComfyCapability})
	if err != nil {
		return "", err
	}
	cfg, err := host.store.Configuration(ctx, localComfyPackage)
	if err != nil {
		return "", err
	}
	e := localComfyInvocation{tool: t, host: host, grant: grant, installed: installed, configRevision: strconv.Itoa(cfg.Revision)}
	if name == localComfyJobTool {
		return e.jobAction(ctx, raw)
	}
	if name == localComfyModelsTool {
		return s.comfyModels(ctx, grant.FishID, raw)
	}
	if name == localComfyFilesTool {
		return s.comfyFiles(ctx, grant.FishID, raw)
	}
	if name == localComfyRegisterTool {
		return s.comfyRegister(ctx, grant.FishID, raw)
	}
	e.deployment, e.workflows, err = s.comfySetup(ctx, grant.FishID)
	if err != nil {
		return "", err
	}
	if name == localComfyListTool {
		if strictLocalMemoryDecode(raw, &struct{}{}) != nil {
			return "", errors.New("工作流列表不需要参数")
		}
		record, err := s.home.identity.Read()
		if err != nil {
			return "", err
		}
		home, err := s.home.directory(record)
		if err != nil {
			return "", err
		}
		return localProcessJSON(map[string]any{"workflows": localComfyWorkflowViews(e.workflows),
			"personal_workflow_directory": filepath.Join(home, "workflows", "comfyui"),
			"personal_registry_format":    "registry.json: {schema_version:1,comfyui_version:<version>,workflows:[{id,label,graph:<filename.json>,sha256:<graph bytes SHA-256>,save_node:<node ID>,parameters:[{id,label,kind,node,input,minimum?,maximum?,max_length?,enum?}]}]}. Use server_write to create graph and registry in personal_workflow_directory, then call project_comfy_workflows again. Existing job outputs are unchanged."})
	}
	return e.start(ctx, raw)
}

type localComfyJobInput struct {
	Origin      *companion.SourceRef `json:"origin"`
	Trace       string               `json:"trace"`
	Binding     string               `json:"binding"`
	Workflow    string               `json:"workflow_id"`
	Approval    string               `json:"workflow_approval"`
	GraphSHA256 string               `json:"effective_sha256"`
	SaveNode    string               `json:"save_node"`
	VideoOutput bool                 `json:"video_output,omitempty"`
	Title       string               `json:"title"`
}

func (e localComfyInvocation) start(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Workflow string                     `json:"workflow_id"`
		Inputs   map[string]json.RawMessage `json:"inputs"`
		Title    string                     `json:"title"`
	}
	if strictLocalMemoryDecode(raw, &in) != nil || in.Workflow == "" {
		return "", errors.New("请选用已批准的 ComfyUI 工作流编号和参数")
	}
	var workflow *localComfyWorkflow
	for i := range e.workflows {
		if e.workflows[i].ID == in.Workflow {
			workflow = &e.workflows[i]
			break
		}
	}
	if workflow == nil {
		return "", errors.New("指定的 ComfyUI 工作流未获批准")
	}
	graph, digest, err := workflow.prepare(in.Inputs)
	if err != nil {
		return "", err
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		in.Title = workflow.Label
	}
	if !safeRVCDisplay(in.Title, 100) {
		return "", errors.New("ComfyUI 标题须为不超过 100 字的单行文字")
	}
	t, g := e.tool, e.grant
	source, ok := t.store.SourceForInput(companion.Input{Channel: "local", Account: g.FishID, Thread: g.OwnerID, Sender: g.OwnerID, TraceID: t.traceID})
	if !ok {
		return "", errors.New("本轮来源已变化，未提交 ComfyUI")
	}
	args := localComfyJobInput{Origin: source, Trace: t.traceID, Binding: e.deployment.Binding, Workflow: in.Workflow,
		Approval: workflow.Fingerprint, GraphSHA256: digest, SaveNode: workflow.SaveNode, VideoOutput: workflow.OutputKind == "video", Title: in.Title}
	op := companion.LocalToolCheckpoint{Tool: localComfyStartTool, Target: in.Workflow, Grant: g.projectJobAuthorization(),
		Key: localCheckpointKey(localComfyStartTool, source.EventID, source.Revision, args)}
	call := pluginhost.Invocation{Account: g.FishID, Thread: g.OwnerID, Requester: g.OwnerID, Purpose: "local ComfyUI creation",
		RequestKey: op.Key, Authorization: op.Grant, SourceVersions: map[string]int{source.EventID: source.Revision}}
	if err := e.authorize(call); err != nil {
		return "", err
	}
	arguments, _ := json.Marshal(args)
	return t.dispatchCheckpointed(ctx, op, g, func() (string, string, error) {
		if err := e.checkSource(ctx, args); err != nil {
			return "", "", err
		}
		job, created, err := e.host.store.CreateJob(ctx, pluginhost.Job{Plugin: localComfyPackage, Version: e.installed.Package.Version,
			PackageDigest: e.installed.Digest, ConfigRevision: e.configRevision, Context: call, Tool: localComfyCapability, Arguments: arguments})
		if err != nil {
			return "", "", err
		}
		var startErr error
		if created {
			job, startErr = e.submit(ctx, job, args, graph)
		}
		view := comfyJobView(job)
		if startErr != nil && job.State == "pending" {
			view["reason"], view["next_step"] = localComfyStartupFailure(startErr)
		}
		value, err := localProcessJSON(view)
		return value, "registered", err
	})
}

func (e localComfyInvocation) authorize(call pluginhost.Invocation) error {
	g := pluginhost.Grant{Account: e.grant.FishID, Plugin: localComfyPackage, Version: e.installed.Package.Version, Permissions: e.installed.Permissions}
	return pluginhost.Authorize(e.installed.Package, g, call, localComfyGrants)
}

func (e localComfyInvocation) checkSource(ctx context.Context, args localComfyJobInput) error {
	t, g := e.tool, e.grant
	if args.Origin == nil || t.run.CheckSource(ctx) != nil || !t.access.current(g) ||
		!t.access.home.artifacts.sourceValid(localArtifact{Fish: g.FishID, Owner: g.OwnerID, Request: args.Trace, Source: args.Origin}) {
		return errors.New("ComfyUI 原请求或本机授权已变化")
	}
	return t.checkProjectRequestMemory(ctx, args.Trace, args.Origin)
}

func (e localComfyInvocation) checkWorkflow(ctx context.Context, args localComfyJobInput) error {
	_, workflows, err := e.tool.projectTools.comfySetup(ctx, e.grant.FishID)
	if err == nil && e.deployment.Binding == args.Binding {
		for _, workflow := range workflows {
			if workflow.ID == args.Workflow && workflow.Fingerprint == args.Approval && (workflow.OutputKind == "video") == args.VideoOutput {
				return nil
			}
		}
	}
	return errors.New("ComfyUI 部署或工作流批准已变化")
}

func (e localComfyInvocation) submit(ctx context.Context, job pluginhost.Job, args localComfyJobInput, graph []byte) (pluginhost.Job, error) {
	ref, err := comfyui.NewJobRef(job.ID, args.SaveNode)
	if err != nil {
		return job, err
	}
	prepare := comfyui.Prepare
	if args.VideoOutput {
		prepare = comfyui.PrepareMP4
	}
	prepared, err := prepare(graph, args.GraphSHA256, ref)
	if err != nil {
		return job, err
	}
	err = e.tool.projectTools.comfy.Use(ctx, e.grant.FishID, args.Binding, true, func(client localComfyClient) error {
		if err := e.checkSource(ctx, args); err != nil {
			return err
		}
		if err := e.checkWorkflow(ctx, args); err != nil {
			return err
		}
		claimed, err := e.host.store.ClaimJob(ctx, job.ID, job.Revision)
		if err != nil {
			return err
		}
		job = claimed
		if err := e.checkSource(ctx, args); err != nil {
			save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			job = e.failComfyJob(save, job, "source_authorization_changed_before_submit")
			return err
		}
		err = client.Submit(ctx, prepared)
		save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		switch {
		case err == nil:
			if next, saveErr := e.host.store.AcknowledgeJob(save, job.ID, job.Revision, ref.ID); saveErr == nil {
				job = next
			}
		case errors.Is(err, comfyui.ErrSubmissionUnknown):
			if next, saveErr := e.host.store.ReviewJobFailure(save, job.ID, job.Revision, pluginhost.JobFailureDispatch); saveErr == nil {
				job = next
			}
		default:
			job = e.failComfyJob(save, job, "workflow_not_queued")
			if job.State == "failed" {
				_ = client.MarkSettled(save, ref)
			}
		}
		return err
	})
	// A startup error before claim leaves a durable, visibly unsubmitted job.
	// It can be cancelled. A new request is required to try the workflow again.
	if err != nil && job.State == "pending" {
		view := job
		view.Diagnostic = &pluginhost.JobDiagnostic{Code: "backend_not_started"}
		return view, err
	}
	return job, err
}

func localComfyStartupFailure(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "comfyui_start_timeout", "ComfyUI 启动超过本轮等待时间；查看工作站 ComfyUI 状态与日志后重试。"
	}
	switch err.Error() {
	case "station_comfyui_port_occupied_no_adoption":
		return "comfyui_port_occupied", "托管 ComfyUI 端口已被其他进程占用；检查工作站中的部署端口和占用进程。"
	case "station_comfyui_start_timeout":
		return "comfyui_start_timeout", "托管 ComfyUI 未在启动时限内就绪；查看工作站 ComfyUI 日志中的启动错误。"
	case "station_comfyui_start_failed_check_private_log":
		return "comfyui_process_start_failed", "解释器或入口未能启动；查看工作站 ComfyUI 日志并核对部署登记。"
	case "station_comfyui_source_unavailable", "station_comfyui_deployment_pin_invalid":
		return "comfyui_runtime_changed", "运行文件缺失或版本已变化；核对工作站 ComfyUI 部署登记。"
	default:
		return "comfyui_start_failed", "查看工作站 ComfyUI 状态和日志，再重试原工作流。"
	}
}

func comfyJobView(job pluginhost.Job) map[string]any {
	view := map[string]any{"job_id": job.ID, "state": job.State}
	if job.State == "completed" && job.CancelRequested {
		view["state"] = "cancelled"
		view["reason"] = "completed_output_revoked"
	}
	if job.State == "submitting" {
		view["state"] = "needs_review"
	}
	if job.CancelRequested && job.Recovery() != "terminal" {
		view["state"] = "cancelling"
	}
	if job.Diagnostic != nil {
		view["reason"] = job.Diagnostic.Code
	}
	if len(job.Result) != 0 && !job.CancelRequested {
		view["verification"] = job.Result
	}
	return view
}

func (t *localToolSet) comfyCheckpointView(op companion.LocalToolCheckpoint, g localWorkspaceGrant) map[string]any {
	view := map[string]any{"tool": op.Tool, "state": "needs_review", "recovered": true}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if t.projectTools == nil || t.run == nil || t.run.CheckSource(ctx) != nil {
		return view
	}
	host, err := t.projectTools.forFish(g.FishID)
	if err != nil {
		return view
	}
	if _, err := installedLocalProject(ctx, host, localProjectOperation{pkg: localComfyPackage, capability: localComfyCapability}); err != nil {
		return view
	}
	job, err := host.store.JobByRequest(ctx, localComfyPackage, g.OwnerID, op.Key)
	if err != nil || job.Context.Authorization != op.Grant || job.Context.Account != g.FishID || job.Context.Requester != g.OwnerID {
		return view
	}
	view = comfyJobView(job)
	view["tool"], view["recovered"] = op.Tool, true
	return view
}
