package station

import (
	"context"
	"encoding/json"
	"errors"

	"wechat-bot/internal/mcptools"
)

func (t *stationAttachmentTools) publicComfyWorkflowTool(ctx context.Context) (mcptools.Tool, bool) {
	if t.projects == nil || t.projectOwnerAllowed == nil || !t.projectOwnerAllowed(t.trace) || !t.allowed() {
		return mcptools.Tool{}, false
	}
	record, err := t.projects.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return mcptools.Tool{}, false
	}
	if _, err = t.projects.publicComfyWorkflows(ctx, record.FishID); err != nil {
		return mcptools.Tool{}, false
	}
	return localComfyWorkflowTool(), true
}

func (t *stationAttachmentTools) callPublicComfyWorkflows(ctx context.Context, raw json.RawMessage) (string, error) {
	if _, ok := t.publicComfyWorkflowTool(ctx); !ok {
		return "", errors.New("工作站 ComfyUI 工作流当前不可用")
	}
	if strictLocalMemoryDecode(raw, &struct{}{}) != nil {
		return "", mcptools.ArgumentError("$", "工作流列表不需要参数")
	}
	record, err := t.projects.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return "", errors.New("工作站身份不可用")
	}
	workflows, err := t.projects.publicComfyWorkflows(ctx, record.FishID)
	if err != nil || !t.projectOwnerAllowed(t.trace) || !t.allowed() {
		return "", errors.New("工作站 ComfyUI 工作流当前不可用")
	}
	return localProcessJSON(map[string]any{"workflows": workflows})
}
