package station

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func (h *localHomeService) stationDocumentAvailable(ctx context.Context, ext string) bool {
	if h.projectTools == nil || !h.projectTools.runtimeReady() || ext == ".pdf" && !h.projectTools.pdfRuntimeReady() {
		return false
	}
	identity, err := h.identity.Read()
	if err != nil || identity.LocalOwner == nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(h.dataDir, "project-tools", identity.FishID)); err != nil {
		return false
	}
	host, err := h.projectTools.forFish(identity.FishID)
	if err != nil {
		return false
	}
	installed, err := installedLocalProject(ctx, host, projectOperation(localDocumentInspectTool))
	return err == nil && slices.Contains(installed.Permissions, "artifact.read")
}

func (h *localHomeService) inspectStationDocument(ctx context.Context, in stationDocumentInput) (string, error) {
	const capability = localDocumentInspect
	operation := projectOperation(localDocumentInspectTool)
	if !h.stationDocumentAvailable(ctx, in.extension) {
		return "", errors.New("当前文档格式的本地文档工坊或运行环境未启用")
	}
	record, err := h.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return "", errors.New("本机主人身份不可用")
	}
	s := h.projectTools
	host, err := s.forFish(record.FishID)
	if err != nil {
		return "", errors.New("文档工坊存储不可用")
	}
	s.update.Lock()
	installed, err := installedLocalProject(ctx, host, operation)
	if err != nil {
		s.update.Unlock()
		return "", err
	}
	release := s.markActive(operation.pkg)
	s.update.Unlock()
	defer release()
	config, err := host.store.Configuration(ctx, operation.pkg)
	if err != nil {
		return "", errors.New("文档工坊配置不可用")
	}
	source := in.record.Source
	invocation := pluginhost.Invocation{
		Account: record.FishID, Requester: record.LocalOwner.ID, Purpose: "inspect authorized incoming WeChat document",
		Thread:        localCheckpointKey("wechat-document", source.Account, source.Dataset, source.Thread),
		Authorization: localCheckpointKey(record.LocalOwner.ID, record.LocalOwner.Revision, source.Grant, in.requester, in.epoch),
		RequestKey: localCheckpointKey(in.record.ID, in.record.SHA256, in.trace, in.page, in.offset, in.limit,
			in.epoch, in.requester, record.LocalOwner.Revision, installed.Revision, config.Revision),
		SourceVersions: in.versions,
	}
	check := func(ctx context.Context, job pluginhost.Job) error {
		if err := in.check(ctx); err != nil {
			return err
		}
		identity, err := h.identity.Read()
		if err != nil || identity.FishID != record.FishID || identity.LocalOwner == nil ||
			identity.LocalOwner.ID != record.LocalOwner.ID || identity.LocalOwner.Revision != record.LocalOwner.Revision {
			return errors.New("本机主人授权已变化")
		}
		current, err := installedLocalProject(ctx, host, operation)
		if err != nil || current.Revision != installed.Revision || !slices.Contains(current.Permissions, "artifact.read") {
			return errors.New("文档工坊版本或授权已变化")
		}
		cfg, err := host.store.Configuration(ctx, operation.pkg)
		if err != nil || cfg.Revision != config.Revision || job.Plugin != operation.pkg || job.Tool != capability ||
			job.PackageDigest != installed.Digest || job.Version != installed.Package.Version ||
			job.ConfigRevision != strconv.Itoa(config.Revision) || !reflect.DeepEqual(job.Context, invocation) {
			return errors.New("文档工坊任务或配置已变化")
		}
		if job.ID != "" {
			fresh, err := host.store.Job(ctx, job.ID)
			if err != nil || fresh.CancelRequested || fresh.Revision != job.Revision {
				return errors.New("文档检查已取消或任务状态已变化")
			}
		}
		return nil
	}
	options := s.options
	if in.extension == ".docx" {
		options.DocumentRuntimeRoot = ""
	}
	controller, input, output, cleanup, err := openScopedProjectRuntime(ctx, filepath.Join(h.dataDir, "project-tool-tasks"),
		host.store, options, operation.pkg, true, in.materialize)
	if err != nil {
		return "", errors.New("无法创建文档检查所需的隔离输入")
	}
	defer cleanup()
	policy := stationDocumentPolicy{in: in, input: input, output: output, check: check}
	if err := controller.EnsureStarted(ctx, operation.pkg, installed.Revision); err != nil {
		return "", errors.New("文档工坊隔离运行器未能启动")
	}
	args := map[string]any{"artifact_id": in.record.ID}
	if in.page == 0 {
		args["text_offset"], args["text_limit"] = in.offset, in.limit
	} else {
		args["pages"] = []int{in.page}
	}
	raw, _ := json.Marshal(args)
	result, err := controller.Invoke(ctx, pluginruntime.CallRequest{Plugin: operation.pkg, Capability: capability, Context: invocation, Arguments: raw}, policy)
	if err != nil {
		return "", err
	}
	job := result.Job
	if err := check(ctx, job); err != nil {
		return "", err
	}
	if job.State == "failed" {
		var reason struct{ Reason string }
		if json.Unmarshal(job.Result, &reason) == nil && pluginruntime.SafeDocumentResultCode(reason.Reason) {
			return "", errors.New("文档工坊读取失败（" + reason.Reason + "）")
		}
	}
	if job.State != "completed" {
		return "", errors.New("文档检查未得到可核对的结果")
	}
	private, err := host.store.PrivateJobResult(ctx, job, check)
	return string(private), err
}

func (in stationDocumentInput) materialize(ctx context.Context, dir string) (localProjectArtifactMaterialized, error) {
	if err := in.check(ctx); err != nil {
		return localProjectArtifactMaterialized{}, err
	}
	path := filepath.Join(dir, "input"+in.extension)
	err := stageStationMedia(ctx, path, in.file, in.record)
	if err == nil {
		err = in.check(ctx)
	}
	if err != nil {
		_ = os.Remove(path)
		return localProjectArtifactMaterialized{}, err
	}
	return localProjectArtifactMaterialized{InputPath: path}, nil
}

type stationDocumentPolicy struct {
	in            stationDocumentInput
	input, output string
	check         func(context.Context, pluginhost.Job) error
}

func (p stationDocumentPolicy) Check(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability) error {
	if cap.ID != localDocumentInspect || cap.Async {
		return errors.New("文档检查能力与任务不匹配")
	}
	return p.check(ctx, job)
}

func (p stationDocumentPolicy) CallMetadata(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability) (map[string]json.RawMessage, error) {
	if err := p.Check(ctx, job, cap); err != nil {
		return nil, err
	}
	mimeType := localDOCXMIME
	if p.in.page != 0 {
		mimeType = "application/pdf"
	}
	out := windowsProjectToolPath(p.output)
	raw, err := json.Marshal(map[string]any{"inputs": []map[string]string{{"artifact_id": p.in.record.ID,
		"path": windowsProjectToolPath(p.input), "sha256": p.in.record.SHA256, "mime_type": mimeType}},
		"input_root": windowsProjectToolPath(filepath.Dir(p.input)), "output_dir": out, "output_root": out})
	return map[string]json.RawMessage{"wechat/document": raw}, err
}

func (p stationDocumentPolicy) Validate(ctx context.Context, job pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	var result pluginruntime.VerifiedResult
	if err := p.Check(ctx, job, cap); err != nil {
		return result, err
	}
	var wire struct {
		OK       bool            `json:"ok"`
		Error    string          `json:"error"`
		Evidence json.RawMessage `json:"evidence"`
		Outputs  []any           `json:"outputs"`
	}
	if len(raw) > 2<<20 || json.Unmarshal(raw, &wire) != nil || len(wire.Outputs) != 0 {
		return result, errors.New("文档工坊返回格式无效")
	}
	if !wire.OK && pluginruntime.SafeDocumentResultCode(wire.Error) {
		result.Failed = true
		result.Result, _ = json.Marshal(map[string]string{"reason": wire.Error})
		return result, nil
	}
	if !wire.OK {
		return result, errors.New("文档工坊未返回可核验的结果")
	}
	content, err := stationDocumentResult(p.in, wire.Evidence)
	if err != nil {
		return result, err
	}
	result.Result = json.RawMessage(`{"status":"inspected"}`)
	result.Private, err = json.Marshal(content)
	return result, err
}

func stationDocumentResult(in stationDocumentInput, evidence json.RawMessage) (map[string]any, error) {
	var result struct {
		Kind      string `json:"kind"`
		SHA256    string `json:"sha256"`
		Bytes     int64  `json:"bytes"`
		Text      string `json:"text"`
		Total     *int   `json:"text_total_characters"`
		Offset    *int   `json:"text_offset"`
		Pages     []int  `json:"pages"`
		PageCount int    `json:"page_count"`
	}
	mismatch := errors.New("文档检查结果的格式、来源或读取范围不匹配")
	if json.Unmarshal(evidence, &result) != nil || result.Kind != strings.TrimPrefix(in.extension, ".") {
		return nil, mismatch
	}
	source := in.record.Source
	view := map[string]any{"status": "inspected", "format": result.Kind, "message_id": source.Message,
		"asset_id": in.record.ID, "sha256": in.record.SHA256, "filename": source.Filename,
		"sender_id": source.Sender, "occurred_at": source.Occurred, "untrusted_document_content": true}
	text := []rune(result.Text)
	total := 0
	if in.page == 0 {
		if result.SHA256 != in.record.SHA256 || result.Bytes != in.record.Bytes || result.Total == nil || result.Offset == nil ||
			*result.Offset != in.offset || *result.Total < in.offset || len(text) != min(in.limit, *result.Total-in.offset) {
			return nil, mismatch
		}
		total = *result.Total
	} else {
		if !slices.Equal(result.Pages, []int{in.page}) || result.PageCount < in.page {
			return nil, mismatch
		}
		total = len(text)
		if in.offset > total {
			return nil, errors.New("正文偏移超过该 PDF 页的文字长度")
		}
		text = text[in.offset : in.offset+min(in.limit, total-in.offset)]
		view["page"], view["page_count"] = in.page, result.PageCount
		if in.offset+len(text) == total && in.page < result.PageCount {
			view["next_page"] = in.page + 1
		}
	}
	if total == 0 {
		view["status"] = "no_extractable_text"
	}
	view["text"], view["text_offset"], view["text_total_characters"] = string(text), in.offset, total
	if in.offset+len(text) < total {
		view["next_text_offset"] = in.offset + len(text)
	}
	return view, nil
}
