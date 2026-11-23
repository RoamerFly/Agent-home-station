package station

import (
	"context"
	"encoding/json"
	"errors"

	"wechat-bot/internal/mcptools"
)

func (t *stationAttachmentTools) publicComfyWorkflowTool(ctx context.Context) (mcptools.Tool, bool) {
	fish, ok := t.publicComfyInstalled(ctx)
	if !ok {
		return mcptools.Tool{}, false
	}
	if _, err := t.projects.publicComfyWorkflows(ctx, fish); err != nil {
		return mcptools.Tool{}, false
	}
	return localComfyWorkflowTool(), true
}

func (t *stationAttachmentTools) publicComfyInstalled(ctx context.Context) (string, bool) {
	if t.projects == nil || !t.allowed() {
		return "", false
	}
	record, err := t.projects.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return "", false
	}
	host, err := t.projects.forFish(record.FishID)
	if err != nil {
		return "", false
	}
	if _, err = installedLocalProject(ctx, host, localProjectOperation{pkg: localComfyPackage, capability: localComfyCapability}); err != nil {
		return "", false
	}
	return record.FishID, true
}

func (t *stationAttachmentTools) publicComfyTools(ctx context.Context) []mcptools.Tool {
	workflow, ok := t.publicComfyWorkflowTool(ctx)
	tools := []mcptools.Tool{}
	if ok {
		tools = append(tools, workflow)
	}
	if t.comfyMu != nil && t.replyAllowed(t.trace) {
		if ok {
			tools = append(tools, localComfyStartToolSpec())
		}
		if _, installed := t.publicComfyInstalled(ctx); installed {
			tools = append(tools, localComfyJobToolSpec())
		}
	}
	return tools
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
	if err != nil || !t.allowed() {
		return "", errors.New("工作站 ComfyUI 工作流当前不可用")
	}
	return localProcessJSON(map[string]any{"workflows": workflows})
}
