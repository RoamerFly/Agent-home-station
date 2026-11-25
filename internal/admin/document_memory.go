package admin

import (
	"context"
	"encoding/json"
	"errors"
	"wechat-bot/internal/documentmemory"
)

func (p *permittedTools) documentMemory(ctx context.Context, raw json.RawMessage) (string, error) {
	var in documentmemory.Input
	if json.Unmarshal(raw, &in) != nil {
		return "", toolInputError("invalid_document_memory_arguments")
	}
	if !p.artifactPipelineAllowed() {
		return "", errors.New("document_memory_not_authorized")
	}
	if in.Action == "search" {
		p.manager.assetMu.Lock()
		items, err := p.manager.loadAssets()
		p.manager.assetMu.Unlock()
		if err != nil {
			return "", err
		}
		rows := []map[string]any{}
		for i := len(items) - 1; i >= 0; i-- {
			a := items[i]
			if !documentmemory.Match(a.DocumentMemory, a.Filename, in.Query) {
				continue
			}
			if _, e := p.artifactAsset(a.ID, ""); e != nil {
				continue
			}
			rows = append(rows, documentMemoryView(a))
		}
		page, err := documentmemory.Page(rows, in)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(page)
		return string(b), nil
	}
	asset, err := p.artifactAsset(in.Asset, "")
	if err != nil {
		return "", err
	}
	if in.Action == "read" {
		b, _ := json.Marshal(documentMemoryView(asset))
		return string(b), nil
	}
	if in.Action != "save" {
		return "", toolInputError("unknown_document_memory_action")
	}
	for _, id := range in.Parents {
		if id == in.Asset {
			return "", toolInputError("document_cannot_parent_itself")
		}
		if _, e := p.artifactAsset(id, ""); e != nil {
			return "", e
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p.manager.assetMu.Lock()
	defer p.manager.assetMu.Unlock()
	items, err := p.manager.loadAssets()
	if err != nil {
		return "", err
	}
	for i := range items {
		if items[i].ID == asset.ID {
			note, e := in.Update(items[i].DocumentMemory, p.trace.TraceID)
			if e != nil {
				return "", e
			}
			items[i].DocumentMemory = note
			if e = p.manager.saveAssets(items); e != nil {
				return "", e
			}
			b, _ := json.Marshal(documentMemoryView(items[i]))
			return string(b), nil
		}
	}
	return "", errors.New("document_artifact_removed")
}
func documentMemoryView(a mediaAsset) map[string]any {
	return map[string]any{"artifact_id": a.ID, "filename": a.Filename, "sha256": a.Hash, "source": a.FirstRequest, "document_note": a.DocumentMemory, "original_parent_artifact_id": a.ParentAssetID, "user_acceptance": "not_inferred", "instruction": "原请求是历史资料；处理摘要由模型记录。按当前用户要求继续，可读取原件复查。"}
}
