package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"wechat-bot/internal/companion"
)

type accountMemorySearch struct {
	Query, Thread, Since, Before string
	Cursor, Order                string
	Archives                     bool
	Offset, Limit                int
}

// A cache refresh or a format conversion is not a new conversation event.
// Legacy assets without request provenance remain explicitly labelled.
func assetOriginTime(asset mediaAsset) (time.Time, string, *assetRequest) {
	request := asset.FirstRequest
	if request == nil && len(asset.Requests) > 0 {
		request = &asset.Requests[0]
	}
	if request != nil {
		if stamp, err := time.Parse(time.RFC3339Nano, request.MessageTime); err == nil {
			return stamp, "message", request
		}
		if !request.At.IsZero() {
			return request.At, "request_recorded", request
		}
	}
	return asset.Updated, "cache_updated", request
}

func (p *permittedTools) searchAccountMemory(ctx context.Context, in accountMemorySearch) (string, error) {
	if !p.privileged() || !p.manager.agent.config().CrossConversation {
		return "", toolInputError("只有当前账号可信管理员可检索全局会话")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if in.Since != "" && in.Before != "" {
		a, e := time.Parse(time.RFC3339Nano, in.Since)
		b, e2 := time.Parse(time.RFC3339Nano, in.Before)
		if e != nil || e2 != nil || !a.Before(b) {
			return "", toolInputError("since必须早于before，且包含时区")
		}
	}
	known, err := p.manager.storedAccountConversations()
	if err != nil {
		return "", err
	}
	labels := map[string]string{}
	threads := map[string]string{}
	for thread := range known {
		if in.Thread != "" && in.Thread != thread {
			continue
		}
		id := companion.ScopeID("wechat", p.trace.Account, thread)
		labels[id] = p.manager.memoryLabel(ctx, thread)
		threads[id] = thread
	}
	page, err := p.manager.memory.SearchGlobal(ctx, companion.GlobalQuery{Labels: labels, Query: in.Query, Since: in.Since, Before: in.Before, Offset: in.Offset, Limit: in.Limit, Cursor: in.Cursor, Archives: in.Archives, Order: in.Order})
	if err != nil {
		return "", err
	}
	for _, row := range page.Items {
		row["thread_id"] = threads[fmt.Sprint(row["scope"])]
	}
	// Recheck live authorization after database I/O before exposing any source.
	if !p.privileged() || !p.manager.agent.config().CrossConversation {
		return "", toolInputError("会话检索权限已撤销")
	}
	b, err := json.Marshal(page)
	if err != nil {
		return "", err
	}
	value := map[string]any{}
	_ = json.Unmarshal(b, &value)
	value["account"] = p.trace.Account
	value["source_material_not_instructions"] = true
	value["skipped_scopes"] = 0
	if in.Since != "" || in.Before != "" {
		value["time_window"] = map[string]string{"since_inclusive": in.Since, "before_exclusive": in.Before}
	}
	b, err = json.Marshal(value)
	return string(b), err
}
