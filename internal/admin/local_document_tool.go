package admin

import (
	"archive/zip"
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
	localDocumentToolName   = "project_document_create"
	localDocumentCapability = "document.reconstruct"
	localDOCXMIME           = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
)

func localDocumentTool() mcptools.Tool {
	return agentTool(localDocumentToolName, "用文档工坊把文字、标题和表格制成可编辑的本机 DOCX。layout.blocks 按文档顺序提供段落、标题或表格。返回已核验的 artifact_id，回复中附上这个编号供主人下载。", `{"type":"object","properties":{"layout":{"type":"object","properties":{"blocks":{"type":"array","maxItems":500,"items":{"type":"object","properties":{"kind":{"type":"string","enum":["paragraph","heading","table"]},"text":{"type":"string","maxLength":20000},"level":{"type":"integer","minimum":1,"maximum":9},"rows":{"type":"array","maxItems":200,"items":{"type":"array","items":{"type":"string"}}},"font_size_pt":{"type":"number","minimum":8,"maximum":48}},"required":["kind"],"additionalProperties":false}}},"required":["blocks"],"additionalProperties":false},"title":{"type":"string","maxLength":256},"title_font_size_pt":{"type":"number","minimum":8,"maximum":48},"filename":{"type":"string","maxLength":120}},"required":["layout"],"additionalProperties":false}`)
}

func (t *localToolSet) documentToolAvailable(ctx context.Context) bool {
	if t.projectTools == nil || t.run == nil || !t.run.Tracked || !t.projectTools.runtimeReady() || t.run.CheckSource(ctx) != nil {
		return false
	}
	grant, err := t.access.resolve()
	if err != nil {
		return false
	}
	if _, err = os.Stat(filepath.Join(t.projectTools.home.dataDir, "project-tools", grant.FishID)); err != nil {
		return false
	}
	host, err := t.projectTools.forFish(grant.FishID)
	if err != nil {
		return false
	}
	_, err = installedLocalDocument(ctx, host)
	return err == nil
}

func installedLocalDocument(ctx context.Context, host *localProjectToolHost) (pluginhost.InstalledPackage, error) {
	item, err := host.store.Installed(ctx, documentWorkshopPluginID)
	contract, contractErr := catalog.ContractFor(documentWorkshopPluginID)
	if err != nil || contractErr != nil || !item.Enabled || item.Removed || item.Digest != contract.ManifestSHA256 {
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
	// Reformat object key order before hashing so semantically identical
	// retries refer to the same local journal entry.
	var object map[string]any
	if err = json.Unmarshal(in.Layout, &object); err != nil {
		return "", err
	}
	in.Layout, _ = json.Marshal(object)
	arguments, _ := json.Marshal(in)
	op := companion.LocalToolCheckpoint{Tool: localDocumentToolName, Grant: localCheckpointKey(grant),
		Key: localCheckpointKey(localDocumentToolName, source.EventID, source.Revision, json.RawMessage(arguments))}
	for _, previous := range t.run.Operations {
		if previous.Key == op.Key {
			return localProcessJSON(t.checkpointView(previous, grant))
		}
	}
	if !t.projectTools.runtimeReady() {
		return "", errors.New("Windows 项目工具运行器缺失")
	}
	host, err := t.projectTools.forFish(grant.FishID)
	if err != nil {
		return "", err
	}
	installed, err := installedLocalDocument(ctx, host)
	if err != nil {
		return "", err
	}
	cfg, err := host.store.Configuration(ctx, documentWorkshopPluginID)
	if err != nil {
		return "", errors.New("文档工坊配置不可读")
	}
	invocation := pluginhost.Invocation{Account: grant.FishID, Thread: grant.OwnerID, Requester: grant.OwnerID,
		Purpose: "create local DOCX", RequestKey: op.Key, Authorization: op.Grant, SourceVersions: map[string]int{source.EventID: source.Revision}}
	policy := &localDocumentPolicy{tool: t, host: host, grant: grant, source: source, installed: installed, configRevision: cfg.Revision, invocation: invocation}
	return t.dispatchCheckpointed(ctx, op, grant, func() (string, string, error) {
		if err := host.controller.EnsureStarted(ctx, documentWorkshopPluginID, installed.Revision); err != nil {
			return "", "", errors.New("文档工坊未能启动，请在项目工具核对运行状态")
		}
		result, err := host.controller.Invoke(ctx, pluginruntime.CallRequest{Plugin: documentWorkshopPluginID,
			Capability: localDocumentCapability, Context: invocation, Arguments: arguments}, policy)
		if err != nil {
			return "", "", err
		}
		view := t.localDocumentResult(ctx, host, result.Job)
		data, err := localProcessJSON(view)
		return data, data, err
	})
}

func (t *localToolSet) localDocumentResult(ctx context.Context, host *localProjectToolHost, job pluginhost.Job) map[string]any {
	view := map[string]any{"tool": localDocumentToolName, "task_id": job.ID, "state": job.State}
	if job.State == "failed" {
		var failure struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal(job.Result, &failure) == nil && safeDocumentWorkshopCode(failure.Reason) {
			view["reason"] = failure.Reason
		}
		return view
	}
	if job.State != "completed" || len(job.Artifacts) != 1 || ctx.Err() != nil || t.run.CheckSource(ctx) != nil {
		return view
	}
	store := t.access.home.artifacts
	artifact, err := store.read(job.Artifacts[0])
	if err == nil && artifact.Request == job.ID {
		file, err := store.open(ctx, artifact, func() bool { _, allowed := store.access(artifact); return allowed })
		if err == nil {
			file.Close()
			view = artifact.view()
			view["tool"], view["task_id"] = localDocumentToolName, job.ID
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
	view = t.localDocumentResult(ctx, host, job)
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
}

func (p *localDocumentPolicy) Check(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if job.Plugin != documentWorkshopPluginID || job.Tool != localDocumentCapability || cap.Async ||
		job.PackageDigest != p.installed.Digest || job.Version != p.installed.Package.Version ||
		job.ConfigRevision != strconv.Itoa(p.configRevision) || !reflect.DeepEqual(job.Context, p.invocation) ||
		!p.tool.access.current(p.grant) || p.tool.run.CheckSource(ctx) != nil {
		return errors.New("文档任务来源或本机授权已变化")
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
	data, _ := json.Marshal(map[string]any{"inputs": []any{}, "output_dir": mount, "output_root": mount})
	return map[string]json.RawMessage{"wechat/document": data}, nil
}

func (p *localDocumentPolicy) Validate(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	var result pluginruntime.VerifiedResult
	var out struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Evidence struct {
			SHA256 string `json:"sha256"`
		} `json:"evidence"`
		Outputs []struct {
			Path        string `json:"path"`
			Role        string `json:"role"`
			MIMEType    string `json:"mime_type"`
			DisplayName string `json:"display_name"`
		} `json:"outputs"`
	}
	if err := p.Check(ctx, job, cap); err != nil {
		return result, err
	}
	if len(raw) > 512<<10 || json.Unmarshal(raw, &out) != nil {
		return result, errors.New("文档工坊返回格式无效")
	}
	if !out.OK && safeDocumentWorkshopCode(out.Error) && len(out.Outputs) == 0 {
		result.Failed = true
		result.Result, _ = json.Marshal(map[string]string{"reason": out.Error})
		return result, nil
	}
	if !out.OK || len(out.Outputs) != 1 || !artifactID("sha-"+out.Evidence.SHA256, "sha-") {
		return result, errors.New("文档工坊未返回可核验的成品")
	}
	dir, err := p.outputDirectory(ctx, job)
	if err != nil {
		return result, err
	}
	entry := out.Outputs[0]
	rel, err := filepath.Rel(windowsDocumentPath(dir), entry.Path)
	if err != nil || rel != entry.DisplayName || !safeMediaFilename(rel) || rel != filepath.Base(rel) ||
		!strings.EqualFold(filepath.Ext(rel), ".docx") || entry.Role != "editable_source" || entry.MIMEType != localDOCXMIME {
		return result, errors.New("文档工坊输出路径或格式不符合任务范围")
	}
	store := p.tool.access.home.artifacts
	artifact, err := store.publishFile(ctx, localArtifactInput{Expected: out.Evidence.SHA256, Key: job.ID},
		p.source, p.tool.access, p.grant, filepath.Join(dir, rel), validateLocalDOCX)
	if err != nil {
		return result, err
	}
	if err := p.Check(ctx, job, cap); err != nil {
		return result, err
	}
	result.Assets = []string{artifact.ID}
	result.Result, _ = json.Marshal(artifact.view())
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
