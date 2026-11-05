package admin

// The artifact pipeline is deliberately a small host primitive.  It gives a
// document (or future) plugin a verified input copy and a single, auditable
// way to return a finished file to the normal media staging path.  It is not a
// second file system API and it never submits a WeChat send.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/plugins"
	"wechat-bot/internal/workspacefs"
)

const artifactPipelineMaxBytes int64 = 512 << 20

// ArtifactMaterialized is the only filesystem hand-off from retained chat
// material to a processor.  Paths are task-scoped and minted by the host.
type ArtifactMaterialized struct {
	AssetID   string
	TaskID    string
	InputPath string
	OutputDir string
	Filename  string
	MIME      string
	Bytes     int64
	SHA256    string
}

// ArtifactPublishRequest names a finished file in the current task output
// directory.  Path is intentionally a basename, not a filesystem path.
type ArtifactPublishRequest struct {
	Path   string
	Title  string
	TaskID string
}

func (p *permittedTools) artifactTools() []mcptools.Tool {
	if !p.artifactPipelineAllowed() {
		return nil
	}
	tools := []mcptools.Tool{agentTool("artifact_materialize", "Make one verified received attachment available to an authorized installed processor in THIS request. asset_id must come from wechat_file_read/media_recent in this conversation. The host keeps the filesystem location private and copies inputs immutably. It never executes document content or sends a message. Input {asset_id:string,source_message_id?:string,task_id?:string}.", `{"type":"object","properties":{"asset_id":{"type":"string","minLength":1,"maxLength":128},"source_message_id":{"type":"string","maxLength":512},"task_id":{"type":"string","maxLength":256}},"required":["asset_id"],"additionalProperties":false}`)}
	if p.manager.Config().Companion.Permissions.SendFile {
		tools = append(tools, agentTool("artifact_publish", "Register one finished regular file already produced by an authorized processor for THIS task and stage it as a file attachment. path is a processor-reported basename, never a filesystem path. This only prepares reply.media_ids; it does NOT send WeChat. Input {path:string,title?:string,task_id?:string}.", `{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":512},"title":{"type":"string","maxLength":512},"task_id":{"type":"string","maxLength":256}},"required":["path"],"additionalProperties":false}`))
	}
	return tools
}

func (p *permittedTools) artifactTaskKey(requested string) (string, error) {
	key := p.trace.TaskID
	if key == "" {
		key = p.trace.OriginTraceID
	}
	if key == "" {
		key = p.trace.TraceID
	}
	if key == "" {
		return "", toolInputError("artifact_request_identity_missing")
	}
	derived := agentID("artifact-task", p.trace.Account, p.trace.Thread, p.trace.Sender, key)[:32]
	// A processor receives the derived, opaque task id from materialization and
	// must be able to return it at publish time.  Do not mistake it for a model
	// supplied cross-task identifier; the current raw task id is also accepted
	// for task continuations.
	if requested != "" && requested != derived && requested != p.trace.TaskID {
		return "", toolInputError("artifact_task_mismatch")
	}
	return derived, nil
}

func (p *permittedTools) artifactPipelineAllowed() bool {
	if !p.workspaceExecutionAllowed() {
		return false
	}
	c := p.manager.agent.config()
	return c.Host.Mode == "files" || c.Host.Mode == "full"
}

func (p *permittedTools) artifactTaskPaths(task string) (string, string, error) {
	if task == "" {
		return "", "", toolInputError("artifact_task_identity_missing")
	}
	base := filepath.Join(p.executionContext().Conversation, "tasks", task)
	in, out := filepath.Join(base, "input"), filepath.Join(base, "output")
	if err := ensureManaged(in, out); err != nil {
		return "", "", err
	}
	return in, out, nil
}

func sameArtifactRequester(a mediaAsset, sender string) bool {
	if a.FirstRequest != nil && a.FirstRequest.RequesterID == sender {
		return true
	}
	for _, r := range a.Requests {
		if r.RequesterID == sender {
			return true
		}
	}
	return false
}

func artifactHasSource(a mediaAsset, id string) bool {
	if id == "" {
		return true
	}
	if a.FirstRequest != nil && a.FirstRequest.TraceID == id {
		return true
	}
	for _, r := range a.Requests {
		if r.TraceID == id {
			return true
		}
	}
	return false
}

func (p *permittedTools) artifactAsset(id, sourceID string) (mediaAsset, error) {
	p.manager.assetMu.Lock()
	defer p.manager.assetMu.Unlock()
	items, err := p.manager.loadAssets()
	if err != nil {
		return mediaAsset{}, err
	}
	for _, a := range items {
		if a.ID != id || a.Account != p.trace.Account || a.Scope != assetScope(p.trace) {
			continue
		}
		if a.Hash == "" || !artifactHasSource(a, sourceID) {
			return mediaAsset{}, toolInputError("artifact_source_not_available")
		}
		// A reference to someone else's upload must come from the actual
		// quoted message in this account and conversation, not a model-supplied id.
		if !sameArtifactRequester(a, p.trace.Sender) {
			quote := p.trace.QuotedSource
			if quote == nil || quote.MessageID == "" || quote.Account != p.trace.Account || quote.Thread != p.trace.Thread || (sourceID != "" && sourceID != quote.MessageID) || !artifactHasSource(a, quote.MessageID) {
				return mediaAsset{}, toolInputError("artifact_requester_not_authorized")
			}
		}
		return a, nil
	}
	return mediaAsset{}, toolInputError("artifact_not_in_current_conversation")
}

func (p *permittedTools) MaterializeArtifactForTask(ctx context.Context, assetID, sourceMessageID, taskID string) (ArtifactMaterialized, error) {
	var zero ArtifactMaterialized
	if !p.artifactPipelineAllowed() {
		return zero, toolInputError("artifact_pipeline_not_authorized")
	}
	if strings.TrimSpace(assetID) == "" || len(sourceMessageID) > 512 {
		return zero, toolInputError("artifact_materialize_arguments_invalid")
	}
	task, err := p.artifactTaskKey(taskID)
	if err != nil {
		return zero, err
	}
	asset, err := p.artifactAsset(assetID, sourceMessageID)
	if err != nil {
		return zero, err
	}
	input, output, err := p.artifactTaskPaths(task)
	if err != nil {
		return zero, err
	}
	if !safeMediaFilename(asset.Filename) || asset.Bytes < 1 || asset.Bytes > artifactPipelineMaxBytes {
		return zero, toolInputError("artifact_source_metadata_invalid")
	}
	root, err := workspacefs.EnsurePath(input)
	if err != nil {
		return zero, err
	}
	defer root.Close()
	src, err := p.manager.openVerifiedAsset(ctx, asset)
	if err != nil {
		return zero, err
	}
	defer src.Close()
	// Keep the real filename in metadata. A bounded disk basename also works
	// for long WeChat attachment names inside Windows AppContainer paths.
	ext := filepath.Ext(asset.Filename)
	if len(ext) > 12 {
		ext = ".bin"
	}
	name := asset.Hash[:12] + ext
	if err = publishVerifiedStream(root, name, src, asset.Bytes, asset.Hash); err != nil {
		return zero, err
	}
	return ArtifactMaterialized{AssetID: asset.ID, TaskID: task, InputPath: filepath.Join(input, name), OutputDir: output, Filename: asset.Filename, MIME: asset.MIME, Bytes: asset.Bytes, SHA256: asset.Hash}, nil
}

func (p *permittedTools) materializeArtifact(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		AssetID string `json:"asset_id"`
		Source  string `json:"source_message_id"`
		TaskID  string `json:"task_id"`
	}
	if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.AssetID) == "" || len(in.Source) > 512 {
		return "", toolInputError("artifact_materialize_arguments_invalid")
	}
	materialized, err := p.MaterializeArtifactForTask(ctx, in.AssetID, in.Source, in.TaskID)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(map[string]any{
		"asset_id": materialized.AssetID, "source_message_id": in.Source, "task_id": materialized.TaskID,
		"filename": materialized.Filename, "mime": materialized.MIME, "bytes": materialized.Bytes,
		"sha256": materialized.SHA256, "original_immutable": true, "ready": true,
		"instruction": "原件已由宿主交给本任务获准的处理器；处理器完成后只报告成品文件名，再调用 artifact_publish 登记。不要索取或猜测本机路径。",
	})
	return string(b), nil
}

func artifactOutputName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !safeMediaFilename(value) || filepath.Base(value) != value || value == "." || value == ".." {
		return "", toolInputError("artifact_output_path_must_be_a_relative_filename")
	}
	return value, nil
}

func fileBytesAndHash(ctx context.Context, path string, limit int64) ([]byte, string, error) {
	root, err := workspacefs.EnsurePath(filepath.Dir(path))
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 || info.Size() > limit {
		return nil, "", toolInputError("artifact_output_not_a_safe_regular_file")
	}
	f, err := workspacefs.OpenRegular(root, name)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, "", errors.New("artifact_output_changed")
	}
	h := sha256.New()
	data, err := io.ReadAll(io.LimitReader(io.TeeReader(f, h), limit+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, "", errors.New("artifact_output_read_failed")
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return data, hex.EncodeToString(h.Sum(nil)), nil
}

func (p *permittedTools) PublishArtifactForTask(ctx context.Context, in ArtifactPublishRequest) (string, error) {
	if !p.artifactPipelineAllowed() || !p.manager.Config().Companion.Permissions.SendFile {
		return "", toolInputError("artifact_pipeline_not_authorized")
	}
	name, err := artifactOutputName(in.Path)
	if err != nil {
		return "", err
	}
	task, err := p.artifactTaskKey(in.TaskID)
	if err != nil {
		return "", err
	}
	_, output, err := p.artifactTaskPaths(task)
	if err != nil {
		return "", err
	}
	data, digest, err := fileBytesAndHash(ctx, filepath.Join(output, name), min(artifactPipelineMaxBytes, toolsLimit("file")))
	if err != nil {
		return "", err
	}
	filename := name
	if safeMediaFilename(strings.TrimSpace(in.Title)) {
		filename = strings.TrimSpace(in.Title)
	}
	typ := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	if typ == "" {
		typ = "application/octet-stream"
	}
	result, err := p.stageOutputWithAsset(ctx, plugins.Output{Text: "成品文件已准备，尚未发送。", Media: []plugins.MediaResult{{Kind: "file", Data: data, MIMEType: typ, Filename: filename}}}, &assetOriginal{Data: data, MIME: typ, Filename: filename})
	if err != nil {
		return "", err
	}
	var out map[string]any
	if json.Unmarshal([]byte(result), &out) != nil {
		return "", errors.New("artifact_publish_result_invalid")
	}
	out["task_id"], out["path"], out["sha256"], out["staged_only"] = task, name, digest, true
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (p *permittedTools) publishArtifact(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Path   string `json:"path"`
		Title  string `json:"title"`
		TaskID string `json:"task_id"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return "", toolInputError("artifact_publish_arguments_invalid")
	}
	return p.PublishArtifactForTask(ctx, ArtifactPublishRequest{Path: in.Path, Title: in.Title, TaskID: in.TaskID})
}

func (p *permittedTools) callArtifact(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if !p.artifactPipelineAllowed() {
		return "", toolInputError("artifact_pipeline_not_authorized")
	}
	switch name {
	case "artifact_materialize":
		return p.materializeArtifact(ctx, raw)
	case "artifact_publish":
		return p.publishArtifact(ctx, raw)
	default:
		return "", toolInputError("unknown_artifact_tool")
	}
}
