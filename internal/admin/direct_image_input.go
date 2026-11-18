package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"wechat-bot/internal/deepseek"
)

func (m *Manager) directImageInputReady() error {
	c := m.Config()
	if c.Provider != "deepseek" || c.Model != "deepseek-flash" {
		return errors.New("direct_image_input_requires_deepseek_flash")
	}
	if !c.Behavior.ReadImages {
		return errors.New("media_understanding_disabled")
	}
	return nil
}

func (p *permittedTools) viewArtifactImage(ctx context.Context, raw json.RawMessage) (string, error) {
	if !p.manager.Config().Behavior.DirectImageInput {
		return "", toolInputError("direct_image_input_disabled")
	}
	if err := p.manager.directImageInputReady(); err != nil {
		return "", err
	}
	var in struct {
		Asset  string `json:"asset_id"`
		Source string `json:"source_message_id"`
		Path   string `json:"path"`
	}
	if json.Unmarshal(raw, &in) != nil || (in.Asset == "") == (in.Path == "") {
		return "", toolInputError("choose_one_asset_id_or_output_basename")
	}
	var data []byte
	var err error
	label := "原图素材 " + in.Asset
	if in.Asset != "" {
		a, e := p.artifactAsset(in.Asset, in.Source)
		if e != nil {
			return "", e
		}
		f, e := p.manager.openVerifiedAsset(ctx, a)
		if e != nil {
			return "", e
		}
		defer f.Close()
		data, err = io.ReadAll(io.LimitReader(f, (32<<20)+1))
	} else {
		name, e := artifactOutputName(in.Path)
		if e != nil {
			return "", e
		}
		task, e := p.artifactTaskKey("")
		if e != nil {
			return "", e
		}
		_, out, e := p.artifactTaskPaths(task)
		if e != nil {
			return "", e
		}
		data, _, err = fileBytesAndHash(ctx, filepath.Join(out, name), 32<<20)
		label = "当前任务生成图片/成品预览 " + name
	}
	if err != nil {
		return "", err
	}
	if err = deepseek.AttachImage(ctx, label, deepseek.Image{Data: data}); err != nil {
		return "", err
	}
	b, _ := json.Marshal(map[string]any{"image_attached": true, "label": label, "bytes": len(data), "mode": "main_model_direct", "instruction": "真实图片将在下一次模型请求中直接显示；请自行观察，不会另调用视觉描述模型。"})
	return string(b), nil
}

func (p *permittedTools) inspectDocumentDirect(ctx context.Context, raw json.RawMessage) (string, error) {
	if err := p.manager.directImageInputReady(); err != nil {
		return "", err
	}
	var in struct {
		Asset  string `json:"artifact_id"`
		Source string `json:"source_message_id"`
		Layout *bool  `json:"include_layout"`
		Pages  []int  `json:"pages"`
		Offset int    `json:"text_offset"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Asset == "" {
		return "", toolInputError("document_asset_required")
	}
	// No silent page slicing: original-file mode reports its actual coverage.
	a, err := p.MaterializeArtifactForTask(ctx, in.Asset, in.Source, "")
	if err != nil {
		return "", err
	}
	data, _, err := fileBytesAndHash(ctx, a.InputPath, 100<<20)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(http.DetectContentType(data), "image/") {
		if err := deepseek.AttachImage(ctx, a.Filename, deepseek.Image{Data: data}); err != nil {
			return "", err
		}
		return `{"mode":"deepseek_original_image","image_attached":true,"instruction":"下一次模型请求直接载入原图，请自行理解。"}`, nil
	}
	p.manager.mu.Lock()
	config := p.manager.config
	p.manager.mu.Unlock()
	model, err := p.manager.makeModel(config)
	if err != nil {
		return "", err
	}
	defer model.CloseIdleConnections()
	layout := strings.EqualFold(filepath.Ext(a.Filename), ".xlsx")
	if in.Layout != nil {
		layout = *in.Layout
	}
	reused, err := model.AttachDocument(ctx, a.Filename, data, layout)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(map[string]any{"mode": "deepseek_original_file", "document_attached": true, "upload_reused": reused, "artifact_id": in.Asset, "bytes": len(data), "requested_pages": in.Pages, "coverage": "original_document", "instruction": "下一次模型请求将直接收到原文档。按当前用户要求理解；pages表示重点页，原文件接口不裁页。文件仅在下一次成功请求携带，需复查时再次调用；不要将上传成功当作已读懂。"})
	return string(b), nil
}
