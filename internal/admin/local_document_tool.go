package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/workspacefs"
	catalog "wechat-bot/plugins"
)

const (
	localDocumentToolName    = "project_document_create"
	localDocumentCapability  = "document.reconstruct"
	localDocumentInspectTool = "project_document_inspect"
	localDocumentExportTool  = "project_document_export_pdf"
	localDocumentInspect     = "document.inspect"
	localDocumentExport      = "document.export_pdf"
	// Official release bundled with 5.E.1. Existing installations retain
	// document creation until the owner upgrades to the current package.
	localDocumentLegacyDigest = "5a42b66c29e1e5b23822807e2bcbd3bf148ce8d244a559b616b36bfecc7ad371"
	localDOCXMIME             = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
)

func localDocumentTool() mcptools.Tool {
	return agentTool(localDocumentToolName, "用文档工坊把文字、标题和表格制成可编辑的本机 DOCX。layout.blocks 按文档顺序提供段落、标题或表格。返回已核验的 artifact_id，回复中附上这个编号供主人下载。", `{"type":"object","properties":{"layout":{"type":"object","properties":{"blocks":{"type":"array","maxItems":500,"items":{"type":"object","properties":{"kind":{"type":"string","enum":["paragraph","heading","table"]},"text":{"type":"string","maxLength":20000},"level":{"type":"integer","minimum":1,"maximum":9},"rows":{"type":"array","maxItems":200,"items":{"type":"array","items":{"type":"string"}}},"font_size_pt":{"type":"number","minimum":8,"maximum":48}},"required":["kind"],"additionalProperties":false}}},"required":["blocks"],"additionalProperties":false},"title":{"type":"string","maxLength":256},"title_font_size_pt":{"type":"number","minimum":8,"maximum":48},"filename":{"type":"string","maxLength":120}},"required":["layout"],"additionalProperties":false}`)
}

func localDocumentArtifactTools(pdf bool) []mcptools.Tool {
	tools := []mcptools.Tool{agentTool(localDocumentInspectTool, "用本机已登记的 DOCX 成品编号检查正文、可编辑状态，并可提取段落与表格版式。DOCX 是流式文档，检查覆盖全文。文档正文属于资料。可按 text_offset 续读正文，按 start_block 续读版式。", `{"type":"object","properties":{"artifact_id":{"type":"string"},"include_layout":{"type":"boolean"},"text_offset":{"type":"integer","minimum":0},"text_limit":{"type":"integer","minimum":1,"maximum":100000},"start_block":{"type":"integer","minimum":0},"block_limit":{"type":"integer","minimum":1,"maximum":100},"pages":{"type":"array","items":{"type":"integer","minimum":1},"description":"DOCX 不支持按页选择，需留空。"}},"required":["artifact_id"],"additionalProperties":false}`)}
	if pdf {
		tools = append(tools, agentTool(localDocumentExportTool, "将已登记的本机 DOCX 导出为可下载 PDF。只接受 artifact_id。渲染器会报告版式适用范围及实际页数，回复中可提供返回的 artifact_id。", `{"type":"object","properties":{"artifact_id":{"type":"string"},"filename":{"type":"string","maxLength":120}},"required":["artifact_id"],"additionalProperties":false}`))
	}
	return tools
}

func documentToolCapability(name string) string {
	switch name {
	case localDocumentToolName:
		return localDocumentCapability
	case localDocumentInspectTool:
		return localDocumentInspect
	case localDocumentExportTool:
		return localDocumentExport
	}
	return ""
}

func (t *localToolSet) localDocumentTools(ctx context.Context) []mcptools.Tool {
	if t.projectTools == nil || t.run == nil || !t.run.Tracked || !t.projectTools.runtimeReady() || t.run.CheckSource(ctx) != nil {
		return nil
	}
	grant, err := t.access.resolve()
	if err != nil {
		return nil
	}
	if _, err = os.Stat(filepath.Join(t.projectTools.home.dataDir, "project-tools", grant.FishID)); err != nil {
		return nil
	}
	host, err := t.projectTools.forFish(grant.FishID)
	if err != nil {
		return nil
	}
	item, err := installedLocalDocument(ctx, host, localDocumentCapability)
	if err != nil {
		return nil
	}
	tools := []mcptools.Tool{localDocumentTool()}
	if item.Digest != localDocumentLegacyDigest {
		tools = append(tools, localDocumentArtifactTools(t.projectTools.pdfRuntimeReady())...)
	}
	return tools
}

func installedLocalDocument(ctx context.Context, host *localProjectToolHost, capability string) (pluginhost.InstalledPackage, error) {
	item, err := host.store.Installed(ctx, documentWorkshopPluginID)
	contract, contractErr := catalog.ContractFor(documentWorkshopPluginID)
	if err != nil || contractErr != nil || !item.Enabled || item.Removed || item.Digest != contract.ManifestSHA256 && (capability != localDocumentCapability || item.Digest != localDocumentLegacyDigest) {
		return item, errors.New("文档工坊未启用或安装版本待核对")
	}
	return item, nil
}

func (t *localToolSet) createLocalDocument(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.projectTools == nil || t.run == nil || !t.run.Tracked {
		return "", errors.New("当前本机对话没有项目工具执行授权")
	}
	var in struct {
		Layout          json.RawMessage `json:"layout"`
		Title           string          `json:"title,omitempty"`
		TitleFontSizePt *float64        `json:"title_font_size_pt,omitempty"`
		Filename        string          `json:"filename,omitempty"`
	}
	var layout struct {
		Blocks []json.RawMessage `json:"blocks"`
	}
	if !utf8.Valid(raw) || strictLocalMemoryDecode(raw, &in) != nil ||
		strictLocalMemoryDecode(in.Layout, &layout) != nil || layout.Blocks == nil || len(layout.Blocks) > 500 ||
		utf8.RuneCountInString(in.Title) > 256 || utf8.RuneCountInString(in.Filename) > 120 ||
		in.TitleFontSizePt != nil && (*in.TitleFontSizePt < 8 || *in.TitleFontSizePt > 48) {
		return "", errors.New("请提供有效的文档版式、标题或文件名。单份最多 500 个版式块")
	}
	for _, block := range layout.Blocks {
		var entry struct {
			Kind     string     `json:"kind"`
			Text     string     `json:"text"`
			Level    *int       `json:"level"`
			Rows     [][]string `json:"rows"`
			FontSize *float64   `json:"font_size_pt"`
		}
		if strictLocalMemoryDecode(block, &entry) != nil || entry.Level != nil && (*entry.Level < 1 || *entry.Level > 9) ||
			entry.FontSize != nil && (*entry.FontSize < 8 || *entry.FontSize > 48) {
			return "", errors.New("文档版式块无效，请先修正版式")
		}
		switch entry.Kind {
		case "paragraph", "heading":
			if utf8.RuneCountInString(entry.Text) > 20000 {
				return "", errors.New("单段最多 20000 个字符，请拆为多个段落")
			}
		case "table":
			if len(entry.Rows) == 0 || len(entry.Rows) > 200 {
				return "", errors.New("单张表格需要 1 至 200 行，可拆为多张表格")
			}
		default:
			return "", errors.New("当前本机文档支持段落、标题和表格")
		}
	}
	if len(layout.Blocks) == 0 && strings.TrimSpace(in.Title) == "" {
		return "", errors.New("请先提供文档正文或标题")
	}
	// Reformat object key order before hashing so semantically identical
	// retries refer to the same local journal entry.
	var object map[string]any
	if err := json.Unmarshal(in.Layout, &object); err != nil {
		return "", err
	}
	in.Layout, _ = json.Marshal(object)
	arguments, _ := json.Marshal(in)
	return t.invokeLocalDocument(ctx, localDocumentToolName, arguments, "")
}

func (t *localToolSet) useLocalDocumentArtifact(ctx context.Context, tool string, raw json.RawMessage) (string, error) {
	var in struct {
		ArtifactID    string `json:"artifact_id"`
		Pages         []int  `json:"pages,omitempty"`
		IncludeLayout *bool  `json:"include_layout,omitempty"`
		TextOffset    *int   `json:"text_offset,omitempty"`
		TextLimit     *int   `json:"text_limit,omitempty"`
		StartBlock    *int   `json:"start_block,omitempty"`
		BlockLimit    *int   `json:"block_limit,omitempty"`
		Filename      string `json:"filename,omitempty"`
	}
	if strictLocalMemoryDecode(raw, &in) != nil || !artifactID(in.ArtifactID, "work-") || utf8.RuneCountInString(in.Filename) > 120 {
		return "", argumentError("$", "需要本机 DOCX 成品编号，文件名最多 120 字符")
	}
	if len(in.Pages) != 0 {
		return "", argumentError("$.pages", "DOCX 为流式文档，请用 text_offset 或 start_block 续读")
	}
	var arguments any
	if tool == localDocumentInspectTool {
		if in.Filename != "" {
			return "", argumentError("$.filename", "检查操作不生成新文件")
		}
		if in.TextOffset != nil && *in.TextOffset < 0 || in.TextLimit != nil && (*in.TextLimit < 1 || *in.TextLimit > 100000) ||
			in.StartBlock != nil && *in.StartBlock < 0 || in.BlockLimit != nil && (*in.BlockLimit < 1 || *in.BlockLimit > 100) {
			return "", argumentError("$", "续读位置须非负，每批正文可读 1–100000 字符，版式可读 1–100 块；可按返回位置续读")
		}
		if (in.StartBlock != nil || in.BlockLimit != nil) && (in.IncludeLayout == nil || !*in.IncludeLayout) {
			return "", argumentError("$.include_layout", "续读版式需要启用 include_layout")
		}
		data, _ := json.Marshal(in)
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(data, &fields)
		delete(fields, "artifact_id")
		arguments = fields
	} else {
		if in.IncludeLayout != nil || in.TextOffset != nil || in.TextLimit != nil || in.StartBlock != nil || in.BlockLimit != nil {
			return "", argumentError("$", "PDF 导出只接受成品编号和文件名，读取请用文档检查")
		}
		arguments = map[string]string{"filename": in.Filename}
	}
	data, _ := json.Marshal(arguments)
	return t.invokeLocalDocument(ctx, tool, data, in.ArtifactID)
}

func (t *localToolSet) invokeLocalDocument(ctx context.Context, tool string, arguments json.RawMessage, inputID string) (string, error) {
	capability := documentToolCapability(tool)
	if capability == "" || t.projectTools == nil || t.run == nil || !t.run.Tracked {
		return "", errors.New("当前本机对话没有项目工具执行授权")
	}
	grant, err := t.access.resolve()
	if err != nil {
		return "", err
	}
	if err := t.run.CheckSource(ctx); err != nil {
		return "", err
	}
	source, ok := t.store.SourceForInput(companion.Input{Channel: "local", Account: grant.FishID, Thread: grant.OwnerID, Sender: grant.OwnerID, TraceID: t.traceID})
	if !ok {
		return "", errors.New("本轮来源已变化")
	}
	versions := map[string]int{source.EventID: source.Revision}
	var input *localProjectArtifactInput
	if inputID != "" {
		input, err = t.access.home.artifacts.bindProjectArtifact(ctx, inputID, t.access, grant)
		if err != nil {
			return "", err
		}
		artifact := input.artifact
		if !strings.EqualFold(filepath.Ext(artifact.Name), ".docx") || artifact.Bytes > 128<<20 {
			return "", argumentError("$.artifact_id", "需要不超过 128 MiB 的已登记 DOCX 成品")
		}
		if artifact.Source != nil {
			versions[artifact.Source.EventID] = artifact.Source.Revision
		}
		var args map[string]json.RawMessage
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", err
		}
		args["artifact"], _ = json.Marshal(localArtifactLink{ID: artifact.ID, SHA256: artifact.SHA256})
		arguments, _ = json.Marshal(args)
	}
	op := companion.LocalToolCheckpoint{Tool: tool, Grant: localCheckpointKey(grant), Target: inputID,
		Key: localCheckpointKey(tool, source.EventID, source.Revision, json.RawMessage(arguments))}
	for _, previous := range t.run.Operations {
		if previous.Key == op.Key {
			return localProcessJSON(t.checkpointView(previous, grant))
		}
	}
	if !t.projectTools.runtimeReady() {
		return "", errors.New("Windows 项目工具运行器缺失")
	}
	if capability == localDocumentExport && !t.projectTools.pdfRuntimeReady() {
		return "", errors.New("当前发行包未配齐 PDF 运行时，请从项目工具查看状态")
	}
	host, err := t.projectTools.forFish(grant.FishID)
	if err != nil {
		return "", err
	}
	installed, err := installedLocalDocument(ctx, host, capability)
	if err != nil {
		return "", err
	}
	cfg, err := host.store.Configuration(ctx, documentWorkshopPluginID)
	if err != nil {
		return "", errors.New("文档工坊配置不可读")
	}
	invocation := pluginhost.Invocation{Account: grant.FishID, Thread: grant.OwnerID, Requester: grant.OwnerID,
		Purpose: "local project " + capability, RequestKey: op.Key, Authorization: op.Grant, SourceVersions: versions}
	policy := &localDocumentPolicy{tool: t, host: host, grant: grant, source: source, installed: installed, configRevision: cfg.Revision,
		invocation: invocation, capability: capability, input: input}
	controller := host.controller
	if input != nil && !t.inspectOnly {
		scoped, closeTask, err := policy.inputRuntime(ctx)
		if err != nil {
			return "", err
		}
		t.projectTools.documentReadActive.Store(true)
		defer t.projectTools.documentReadActive.Store(false)
		defer closeTask()
		controller = scoped
	}
	return t.dispatchCheckpointed(ctx, op, grant, func() (string, string, error) {
		if err := controller.EnsureStarted(ctx, documentWorkshopPluginID, installed.Revision); err != nil {
			return "", "", errors.New("文档工坊未能启动，请在项目工具核对运行状态")
		}
		result, err := controller.Invoke(ctx, pluginruntime.CallRequest{Plugin: documentWorkshopPluginID,
			Capability: capability, Context: invocation, Arguments: arguments}, policy)
		if err != nil {
			return "", "", err
		}
		if input != nil {
			if err := input.check(ctx); err != nil {
				return "", "", err
			}
		}
		view := t.localDocumentResult(ctx, tool, result.Job)
		data, err := localProcessJSON(view)
		return data, "recorded", err
	})
}

func (t *localToolSet) localDocumentResult(ctx context.Context, tool string, job pluginhost.Job) map[string]any {
	view := map[string]any{"tool": tool, "task_id": job.ID, "state": job.State}
	if job.State == "failed" {
		var failure struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal(job.Result, &failure) == nil && safeDocumentWorkshopCode(failure.Reason) {
			view["reason"] = failure.Reason
		}
		return view
	}
	if job.State != "completed" || ctx.Err() != nil || t.run.CheckSource(ctx) != nil {
		return view
	}
	if tool == localDocumentInspectTool && len(job.Artifacts) == 0 {
		view["status"], view["evidence"] = "inspected", json.RawMessage(job.Result)
		return view
	}
	if len(job.Artifacts) != 1 {
		return view
	}
	store := t.access.home.artifacts
	artifact, err := store.read(job.Artifacts[0])
	if err == nil && artifact.Request == job.ID {
		file, err := store.open(ctx, artifact, func() bool { _, allowed := store.access(artifact); return allowed })
		if err == nil {
			file.Close()
			view = artifact.view()
			view["tool"], view["task_id"] = tool, job.ID
			if tool == localDocumentExportTool {
				view["verification"] = json.RawMessage(job.Result)
			}
			return view
		}
	}
	view["state"] = "artifact_unavailable"
	return view
}

func (t *localToolSet) documentCheckpointView(op companion.LocalToolCheckpoint, grant localWorkspaceGrant) map[string]any {
	view := map[string]any{"tool": op.Tool, "state": "needs_review", "recovered": true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if t.projectTools == nil || t.run == nil || t.run.CheckSource(ctx) != nil {
		return view
	}
	host, err := t.projectTools.forFish(grant.FishID)
	if err != nil {
		return view
	}
	job, err := host.store.JobByRequest(ctx, documentWorkshopPluginID, grant.OwnerID, op.Key)
	if err != nil || job.Context.Authorization != op.Grant || job.Context.Requester != grant.OwnerID || job.Context.Account != grant.FishID {
		return view
	}
	if job.Tool != documentToolCapability(op.Tool) {
		return view
	}
	if op.Target != "" {
		var args struct {
			Artifact localArtifactLink `json:"artifact"`
		}
		artifact, err := t.access.home.artifacts.bindProjectArtifact(ctx, op.Target, t.access, grant)
		if err != nil || json.Unmarshal(job.Arguments, &args) != nil ||
			args.Artifact.ID != op.Target || args.Artifact.SHA256 != artifact.artifact.SHA256 {
			view["state"] = "artifact_unavailable"
			return view
		}
	}
	view = t.localDocumentResult(ctx, op.Tool, job)
	view["recovered"] = true
	return view
}

type localDocumentPolicy struct {
	tool           *localToolSet
	host           *localProjectToolHost
	grant          localWorkspaceGrant
	source         *companion.SourceRef
	installed      pluginhost.InstalledPackage
	configRevision int
	invocation     pluginhost.Invocation
	capability     string
	input          *localProjectArtifactInput
	inputPath      string
	outputPath     string
}

// Reading an existing private artifact requires a task-scoped LPAC mount.
// The long-lived creation worker never sees input copies from other requests.
func (p *localDocumentPolicy) inputRuntime(ctx context.Context) (*pluginruntime.Controller, func(), error) {
	parent := filepath.Join(p.tool.projectTools.home.dataDir, "doc-tasks")
	root, err := workspacefs.EnsurePath(parent)
	if err != nil {
		return nil, nil, err
	}
	if err := root.Close(); err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp(parent, "task-")
	if err != nil {
		return nil, nil, err
	}
	inputDir := filepath.Join(dir, "input")
	p.outputPath = filepath.Join(dir, "output")
	var controller *pluginruntime.Controller
	cleanup := func() {
		if controller != nil {
			_ = controller.Close()
		}
		if p.inputPath != "" {
			_ = os.Remove(p.inputPath)
		}
		_ = os.Remove(inputDir)
		_ = os.Remove(p.outputPath)
		_ = os.Remove(dir)
	}
	if err := os.Mkdir(inputDir, 0700); err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := os.Mkdir(p.outputPath, 0700); err != nil {
		cleanup()
		return nil, nil, err
	}
	materialized, err := p.input.materialize(ctx, inputDir)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	p.inputPath = materialized.InputPath
	options := p.tool.projectTools.options
	if p.capability != localDocumentExport {
		options.DocumentRuntimeRoot = ""
	}
	executor := pluginruntime.InstalledExecutor(p.host.store, options)
	resolve := executor.Resolve
	executor.Resolve = func(ctx context.Context, pkg pluginhost.Package, grant pluginhost.Grant) (pluginruntime.LaunchSpec, error) {
		spec, err := resolve(ctx, pkg, grant)
		if err != nil || pkg.ID != documentWorkshopPluginID {
			return spec, errors.New("文档工坊隔离环境不可用")
		}
		spec.DataDir = p.outputPath
		spec.ReadOnlyDirs = append(spec.ReadOnlyDirs, inputDir)
		spec.ReadOnlyFiles = append(spec.ReadOnlyFiles, p.inputPath)
		return spec, nil
	}
	controller, err = pluginruntime.NewController(p.host.store, executor)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return controller, cleanup, nil
}

func (p *localDocumentPolicy) Check(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if job.Plugin != documentWorkshopPluginID || job.Tool != p.capability || cap.Async ||
		job.PackageDigest != p.installed.Digest || job.Version != p.installed.Package.Version ||
		job.ConfigRevision != strconv.Itoa(p.configRevision) || !reflect.DeepEqual(job.Context, p.invocation) ||
		!p.tool.access.current(p.grant) || p.tool.run.CheckSource(ctx) != nil {
		return errors.New("文档任务来源或本机授权已变化")
	}
	if p.input != nil {
		if err := p.input.check(ctx); err != nil {
			return err
		}
	}
	current, err := p.host.store.Installed(ctx, job.Plugin)
	if err != nil || !current.Enabled || current.Removed || current.Revision != p.installed.Revision || current.Digest != p.installed.Digest {
		return errors.New("文档工坊版本或授权已变化")
	}
	cfg, err := p.host.store.Configuration(ctx, job.Plugin)
	if err != nil || cfg.Revision != p.configRevision {
		return errors.New("文档工坊配置已变化")
	}
	if job.ID != "" {
		currentJob, err := p.host.store.Job(ctx, job.ID)
		if err != nil || currentJob.Revision != job.Revision || currentJob.CancelRequested {
			return errors.New("文档任务已取消或状态变化")
		}
	}
	return nil
}

func (p *localDocumentPolicy) outputDirectory(ctx context.Context, job pluginhost.Job) (string, error) {
	if p.outputPath != "" {
		return p.outputPath, nil
	}
	if !artifactID("job-"+job.ID, "job-") {
		return "", errors.New("文档任务编号无效")
	}
	private, err := p.host.store.PrivateDirectory(ctx, job.Plugin)
	if err != nil {
		return "", err
	}
	return filepath.Join(private, job.ID), nil
}

func (p *localDocumentPolicy) CallMetadata(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability) (map[string]json.RawMessage, error) {
	if err := p.Check(ctx, job, cap); err != nil {
		return nil, err
	}
	dir, err := p.outputDirectory(ctx, job)
	if err != nil {
		return nil, err
	}
	// The process creates the child directory inside its LPAC-mounted data
	// root. Host-created children are not guaranteed to inherit the live SID
	// grant. The registrar opens the returned file without following links.
	mount := windowsDocumentPath(dir)
	metadata := map[string]any{"inputs": []any{}, "output_dir": mount, "output_root": mount}
	if p.input != nil {
		artifact := p.input.artifact
		metadata["inputs"] = []map[string]string{{"artifact_id": artifact.ID, "sha256": artifact.SHA256,
			"path": windowsDocumentPath(p.inputPath), "mime_type": localDOCXMIME}}
		metadata["input_root"] = windowsDocumentPath(filepath.Dir(p.inputPath))
	}
	data, _ := json.Marshal(metadata)
	return map[string]json.RawMessage{"wechat/document": data}, nil
}

func (p *localDocumentPolicy) Validate(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	var result pluginruntime.VerifiedResult
	var out struct {
		OK       bool            `json:"ok"`
		Error    string          `json:"error"`
		Evidence json.RawMessage `json:"evidence"`
		Outputs  []struct {
			Path        string `json:"path"`
			Role        string `json:"role"`
			MIMEType    string `json:"mime_type"`
			DisplayName string `json:"display_name"`
		} `json:"outputs"`
	}
	if err := p.Check(ctx, job, cap); err != nil {
		return result, err
	}
	if len(raw) > 2<<20 || json.Unmarshal(raw, &out) != nil {
		return result, errors.New("文档工坊返回格式无效")
	}
	if !out.OK && safeDocumentWorkshopCode(out.Error) && len(out.Outputs) == 0 {
		result.Failed = true
		result.Result, _ = json.Marshal(map[string]string{"reason": out.Error})
		return result, nil
	}
	if !out.OK {
		return result, errors.New("文档工坊未返回可核验的结果")
	}
	var evidence struct {
		SHA256 string `json:"sha256"`
		Export struct {
			SHA256          string `json:"sha256"`
			Backend         string `json:"backend"`
			SupportedLayout string `json:"supported_layout"`
			Bytes           int64  `json:"bytes"`
		} `json:"export"`
		Verification struct {
			OK        bool   `json:"ok"`
			SHA256    string `json:"sha256"`
			Bytes     int64  `json:"bytes"`
			PageCount int    `json:"page_count"`
		} `json:"verification"`
	}
	if json.Unmarshal(out.Evidence, &evidence) != nil {
		return result, errors.New("文档工坊证据格式无效")
	}
	if p.capability == localDocumentInspect {
		if len(out.Outputs) != 0 || evidence.SHA256 != p.input.artifact.SHA256 {
			return result, errors.New("文档检查结果与输入成品不匹配")
		}
		result.Result = out.Evidence
		return result, nil
	}
	extension, role, mimeType, digest := ".docx", "editable_source", localDOCXMIME, evidence.SHA256
	validate := validateLocalDOCX
	var parent *localArtifactLink
	if p.capability == localDocumentExport {
		extension, role, mimeType, digest = ".pdf", "delivery_pdf", "application/pdf", evidence.Export.SHA256
		if !evidence.Verification.OK || evidence.Verification.PageCount < 1 || evidence.Export.Bytes < 1 ||
			digest != evidence.Verification.SHA256 || evidence.Export.Bytes != evidence.Verification.Bytes {
			return result, errors.New("PDF 导出与验证回执不一致")
		}
		parent = &localArtifactLink{ID: p.input.artifact.ID, SHA256: p.input.artifact.SHA256}
		validate = func(file *os.File, size int64) error {
			var header [5]byte
			if _, err := file.ReadAt(header[:], 0); err != nil || size != evidence.Export.Bytes || !bytes.Equal(header[:], []byte("%PDF-")) {
				return errors.New("成品的 PDF 格式或大小与回执不一致")
			}
			return nil
		}
	}
	if len(out.Outputs) != 1 || !artifactID("sha-"+digest, "sha-") {
		return result, errors.New("文档工坊未返回可核验的成品")
	}
	dir, err := p.outputDirectory(ctx, job)
	if err != nil {
		return result, err
	}
	entry := out.Outputs[0]
	rel, err := filepath.Rel(windowsDocumentPath(dir), windowsDocumentPath(entry.Path))
	if err != nil || !filepath.IsAbs(entry.Path) || rel != entry.DisplayName || !safeMediaFilename(rel) || rel != filepath.Base(rel) ||
		!strings.EqualFold(filepath.Ext(rel), extension) || entry.Role != role || entry.MIMEType != mimeType {
		return result, errors.New("文档工坊输出路径或格式不符合任务范围")
	}
	p.tool.projectTools.update.Lock()
	defer p.tool.projectTools.update.Unlock()
	if err := p.Check(ctx, job, cap); err != nil {
		return result, err
	}
	store := p.tool.access.home.artifacts
	artifact, err := store.publishFile(ctx, localArtifactInput{Expected: digest, Key: job.ID, parent: parent},
		p.source, p.tool.access, p.grant, filepath.Join(dir, rel), validate)
	if err != nil {
		return result, err
	}
	if err := p.Check(ctx, job, cap); err != nil {
		return result, err
	}
	result.Assets = []string{artifact.ID}
	if p.capability == localDocumentExport {
		result.Result, _ = json.Marshal(map[string]any{"page_count": evidence.Verification.PageCount,
			"backend": evidence.Export.Backend, "supported_layout": evidence.Export.SupportedLayout})
	} else {
		result.Result, _ = json.Marshal(artifact.view())
	}
	// Only the verified filename is removed. An interrupted, ambiguous job
	// remains in its private directory for diagnosis and is never replayed.
	if root, err := workspacefs.OpenPath(dir); err == nil {
		_ = root.Remove(rel)
		root.Close()
		_ = os.Remove(dir)
	}
	return result, nil
}

// CPython running inside LPAC can hit MAX_PATH even on machines with the
// long-path registry flag. Pass extended paths to the tool, and use ordinary
// host paths for the separately validated artifact read.
func windowsDocumentPath(path string) string {
	if strings.HasPrefix(path, `\\?\`) {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(path, `\\`)
	}
	return `\\?\` + path
}

func validateLocalDOCX(file *os.File, size int64) error {
	doc, err := zip.NewReader(file, size)
	if err == nil {
		found := map[string]bool{}
		for _, part := range doc.File {
			switch part.Name {
			case "[Content_Types].xml", "_rels/.rels", "word/document.xml":
				if part.UncompressedSize64 > 0 {
					found[part.Name] = true
				}
			}
		}
		if len(found) == 3 {
			return nil
		}
	}
	return errors.New("生成文件缺少有效的 DOCX 文档结构")
}
