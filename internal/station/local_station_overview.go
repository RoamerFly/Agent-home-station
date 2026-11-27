package station

import (
	"context"
	"encoding/json"
	"strings"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/mcptools"
)

const localStationOverviewTool = "station_overview"

func stationOverviewTool() mcptools.Tool {
	return mcptools.NewTool(localStationOverviewTool,
		"按需查看工作站和已接入应用的当前状态。scope=apps 可指定 app_id；scope=tools 可指定 tool_id。只返回状态，不载入聊天、作品或应用记忆；具体内容再用 app_memory、app_observe 或作品工具读取。",
		`{"type":"object","properties":{"scope":{"type":"string","enum":["apps","tools"]},"app_id":{"type":"string"},"tool_id":{"type":"string"}},"required":["scope"],"additionalProperties":false}`)
}

type stationOverviewInput struct {
	Scope  string `json:"scope"`
	AppID  string `json:"app_id"`
	ToolID string `json:"tool_id"`
}

func (t *localToolSet) callStationOverview(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.publications == nil || t.run == nil || t.access == nil || t.checkAppObservationIdentity() != nil {
		return "", apphost.ErrObservationForbidden
	}
	if err := t.run.CheckSource(ctx); err != nil {
		return "", err
	}
	var in stationOverviewInput
	if err := strictLocalMemoryDecode(raw, &in); err != nil {
		return "", err
	}
	switch in.Scope {
	case "apps":
		if in.ToolID != "" {
			return "", apphost.ErrObservationInvalid
		}
		result, err := t.publications.applicationOverview(ctx, in.AppID)
		if err != nil {
			return "", err
		}
		if err := t.run.CheckSource(ctx); err != nil {
			return "", err
		}
		if t.checkAppObservationIdentity() != nil {
			return "", apphost.ErrObservationForbidden
		}
		body, err := json.Marshal(result)
		return string(body), err
	case "tools":
		if in.AppID != "" {
			return "", apphost.ErrObservationInvalid
		}
		request, _ := json.Marshal(map[string]string{"action": "status", "tool_id": in.ToolID})
		return t.callStationLifecycle(ctx, "station_tool_manage", request)
	default:
		return "", apphost.ErrObservationInvalid
	}
}

// applicationOverview is deliberately invoked only by the status tool. The
// per-turn home catalog contains names and switch state, never a connection
// probe or the contents of another application's conversations.
func (h *localChatService) applicationOverview(ctx context.Context, appID string) (map[string]any, error) {
	if h == nil {
		return nil, apphost.ErrUnavailable
	}
	apps := h.applications.Applications()
	if appID != "" {
		if _, ok := h.applications.Lookup(appID); !ok {
			return nil, apphost.ErrNotFound
		}
	}
	items := make([]map[string]any, 0, len(apps))
	for _, app := range apps {
		if appID != "" && app.ID != appID {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry, ok := h.applications.Lookup(app.ID)
		if !ok {
			continue
		}
		item := map[string]any{"id": app.ID, "name": app.Name, "registration": app.Registration}
		if h.wechat != nil && app.ID == "wechat" {
			item["switch"] = h.wechat.appPower().state()
		}
		connections, err := entry.Provider.Connections(ctx)
		if err != nil {
			item["state"] = "unavailable"
			items = append(items, item)
			continue
		}
		statuses := make([]map[string]any, 0, len(connections))
		for _, connection := range connections {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			status, err := entry.Provider.Inspect(ctx, connection.ID)
			if err != nil {
				statuses = append(statuses, map[string]any{"id": connection.ID, "state": "unavailable"})
				continue
			}
			row := map[string]any{"id": connection.ID}
			for key, value := range map[string]string{"login": status.Login, "runtime": status.Runtime, "worker": status.Worker, "capability_check": status.CapabilityCheck} {
				if value != "" {
					row[key] = value
				}
			}
			if status.SubmissionsPaused {
				row["submissions_paused"] = true
			}
			if reason := strings.TrimSpace(status.WorkerBlocker); reason != "" {
				row["worker_blocker"] = reason
			}
			statuses = append(statuses, row)
		}
		item["state"] = "connected"
		if len(connections) == 0 {
			item["state"] = "not_connected"
		}
		item["connections"] = statuses
		items = append(items, item)
	}
	return map[string]any{"kind": "station_overview", "scope": "apps", "items": items, "note": "状态来自本次按需查询；聊天和作品内容没有载入。"}, nil
}
