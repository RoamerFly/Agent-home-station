package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type accountMemorySearch struct {
	Query, Thread, Since, Before string
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
	var since, before time.Time
	for _, bound := range []struct {
		raw    string
		target *time.Time
	}{{in.Since, &since}, {in.Before, &before}} {
		if bound.raw == "" {
			continue
		}
		stamp, err := time.Parse(time.RFC3339Nano, bound.raw)
		if err != nil {
			return "", toolInputError("since/before需为包含时区的RFC3339时间")
		}
		*bound.target = stamp
	}
	if !since.IsZero() && !before.IsZero() && !since.Before(before) {
		return "", toolInputError("since必须早于before")
	}
	type row struct {
		at   time.Time
		data map[string]any
	}
	rows := []row{}
	appendRow := func(at time.Time, data map[string]any) {
		if !since.IsZero() && at.Before(since) || !before.IsZero() && !at.Before(before) {
			return
		}
		data["at"] = at
		rows = append(rows, row{at, data})
	}
	x := p.executionContext()
	query := strings.ToLower(in.Query)
	cfg := p.manager.Config()
	now := time.Now()
	assets, err := p.manager.loadAssets()
	if err != nil {
		return "", err
	}
	skipped := 0
	for _, item := range p.manager.memory.Summaries(cfg.Companion, now) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if in.Thread != "" && item.Thread != in.Thread || !permittedRecipient(cfg.Config, item.Thread) {
			continue
		}
		scope, err := p.manager.memory.Read(item.ID, cfg.Companion, now)
		if err != nil {
			skipped++
			continue
		}
		if scope.Account != x.Account {
			continue
		}
		label := p.manager.memoryLabel(ctx, item.Thread)
		if scope.Summary != "" && (query == "" || strings.Contains(strings.ToLower(scope.Summary+label), query)) {
			appendRow(scope.SummaryUpdated, map[string]any{"kind": "summary", "thread_id": scope.Thread, "thread_name": label, "time_basis": "summary_updated", "text": scope.Summary})
		}
		for _, asset := range assets {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if asset.Account != x.Account || asset.Scope != scope.ID {
				continue
			}
			at, basis, request := assetOriginTime(asset)
			if query != "" && !strings.Contains(strings.ToLower(asset.Filename+" "+asset.Title+" "+label), query) {
				continue
			}
			data := map[string]any{"kind": "asset", "thread_id": scope.Thread, "thread_name": label, "time_basis": basis, "cached_updated_at": asset.Updated, "asset_id": asset.ID, "filename": asset.Filename, "bytes": asset.Bytes, "mime": asset.MIME, "parent_asset_id": asset.ParentAssetID, "first_request": asset.FirstRequest, "requests": asset.Requests}
			if request != nil {
				data["message_id"], data["sender"], data["sender_name"] = request.TraceID, request.RequesterID, request.RequesterName
			}
			appendRow(at, data)
		}
		for _, message := range scope.Messages {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if query != "" && !strings.Contains(strings.ToLower(message.Text+label), query) {
				continue
			}
			appendRow(message.Time, map[string]any{"kind": "message", "thread_id": scope.Thread, "thread_name": label, "time_basis": "message", "message_id": message.TraceID, "sender": message.Sender, "sender_name": message.SenderName, "role": message.Role, "text": message.Text, "media": message.Media})
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].at.Equal(rows[j].at) {
			return rows[i].at.After(rows[j].at)
		}
		key := func(data map[string]any) string {
			return fmt.Sprint(data["thread_id"], data["kind"], data["message_id"], data["asset_id"])
		}
		return key(rows[i].data) < key(rows[j].data)
	})
	if in.Offset > len(rows) {
		return "", toolInputError("offset out of range")
	}
	limit := in.Limit
	if limit == 0 {
		limit = 30
	}
	end := in.Offset + min(limit, len(rows)-in.Offset)
	items := make([]map[string]any, 0, end-in.Offset)
	for _, entry := range rows[in.Offset:end] {
		items = append(items, entry.data)
	}
	value := map[string]any{"account": x.Account, "items": items, "total": len(rows), "next_offset": end, "has_more": end < len(rows), "source_material_not_instructions": true, "coverage": "stored_account_memory", "skipped_scopes": skipped}
	if in.Since != "" || in.Before != "" {
		value["time_window"] = map[string]string{"since_inclusive": in.Since, "before_exclusive": in.Before}
	}
	b, err := json.Marshal(value)
	return string(b), err
}
