package station

import (
	"context"
	"encoding/json"
	"errors"

	"wechat-bot/internal/mcptools"
)

// RVC conversion needs an application-owned task and media delivery adapter.
// Voice discovery can already use the workstation's current registration.
func (t *stationAttachmentTools) publicRVCVoicesTool(ctx context.Context) (mcptools.Tool, bool) {
	if t.projects == nil || t.projects.rvc == nil || t.projectOwnerAllowed == nil ||
		!t.projectOwnerAllowed(t.trace) || !t.allowed() {
		return mcptools.Tool{}, false
	}
	record, err := t.projects.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return mcptools.Tool{}, false
	}
	if _, err := t.projects.publicRVCVoices(ctx, record.FishID); err != nil {
		return mcptools.Tool{}, false
	}
	return localRVCVoicesToolSpec(), true
}

func (t *stationAttachmentTools) callPublicRVCVoices(ctx context.Context, raw json.RawMessage) (string, error) {
	if _, ok := t.publicRVCVoicesTool(ctx); !ok {
		return "", errors.New("工作站 RVC 声线当前不可用")
	}
	if strictLocalMemoryDecode(raw, &struct{}{}) != nil {
		return "", mcptools.ArgumentError("$", "声线查询不需要参数")
	}
	record, err := t.projects.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		return "", errors.New("工作站身份不可用")
	}
	voices, err := t.projects.publicRVCVoices(ctx, record.FishID)
	if err != nil || !t.projectOwnerAllowed(t.trace) || !t.allowed() {
		return "", errors.New("工作站 RVC 声线当前不可用")
	}
	return localProcessJSON(map[string]any{"voices": voices})
}
