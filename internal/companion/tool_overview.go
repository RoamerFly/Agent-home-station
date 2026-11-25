package companion

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"wechat-bot/internal/mcptools"
)

// toolOverviewProvider may add a compact host snapshot to the already granted
// tool catalogue. The catalogue alone never claims that a service is running.
type toolOverviewProvider interface {
	ToolOverview(context.Context) (map[string]any, error)
}

type toolOverviewRequest struct {
	IncludeNames bool   `json:"include_names"`
	Group        string `json:"group"`
	Offset       int    `json:"offset"`
	Limit        int    `json:"limit"`
}

type toolOverviewGroup struct {
	Group string `json:"group"`
	Count int    `json:"count"`
}

// Put host state before the optionally expanded names. A long name list must
// not hide the service snapshot when feedback is byte-bounded.
type toolOverviewResult struct {
	Host                map[string]any      `json:"host,omitempty"`
	HostStatus          string              `json:"host_status,omitempty"`
	Scope               string              `json:"scope"`
	AuthorizedToolCount int                 `json:"authorized_tool_count"`
	Groups              []toolOverviewGroup `json:"groups"`
	Names               []string            `json:"names,omitempty"`
	NameGroup           string              `json:"name_group,omitempty"`
	Offset              int                 `json:"offset,omitempty"`
	NextOffset          int                 `json:"next_offset,omitempty"`
	HasMore             bool                `json:"has_more,omitempty"`
	EffectiveLimit      int                 `json:"effective_limit,omitempty"`
	LimitReason         string              `json:"limit_reason,omitempty"`
	DetailTool          string              `json:"detail_tool"`
	HealthProbed        bool                `json:"health_probed"`
}

func overviewGroup(name string) string {
	parts := strings.SplitN(name, "_", 3)
	if len(parts) > 1 && (parts[0] == "station" || parts[0] == "agent") {
		return parts[0] + "_" + parts[1]
	}
	return parts[0]
}

func (e *AgentRunner) toolOverview(ctx context.Context, granted []mcptools.Tool, raw json.RawMessage) (string, error) {
	var query toolOverviewRequest
	if err := json.Unmarshal(raw, &query); err != nil || query.Offset < 0 || query.Limit < 0 {
		return "", errors.New("tool_overview_arguments_invalid")
	}
	groups := map[string][]string{}
	for _, tool := range granted {
		group := overviewGroup(tool.Name)
		groups[group] = append(groups[group], tool.Name)
	}
	groupNames := make([]string, 0, len(groups))
	for name := range groups {
		groupNames = append(groupNames, name)
		sort.Strings(groups[name])
	}
	sort.Strings(groupNames)
	rows := make([]toolOverviewGroup, 0, len(groupNames))
	for _, name := range groupNames {
		rows = append(rows, toolOverviewGroup{Group: name, Count: len(groups[name])})
	}
	result := toolOverviewResult{
		Scope: "current_conversation_authorized", AuthorizedToolCount: len(granted),
		Groups: rows, DetailTool: "discover_tools", HealthProbed: false,
	}
	if provider, ok := e.Tools.(toolOverviewProvider); ok {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if host, err := provider.ToolOverview(probeCtx); err == nil {
			result.Host = host
		} else {
			result.HostStatus = "unavailable"
		}
	}
	if query.IncludeNames || query.Group != "" {
		selected := []string{}
		if query.Group != "" {
			selected = append(selected, groups[query.Group]...)
			result.NameGroup = query.Group
		} else {
			for _, tool := range granted {
				selected = append(selected, tool.Name)
			}
			sort.Strings(selected)
		}
		if query.Offset > len(selected) {
			return "", errors.New("tool_overview_offset_exceeds_names")
		}
		limit := query.Limit
		if limit == 0 {
			limit = 40
		}
		end := query.Offset + min(limit, len(selected)-query.Offset)
		result.Offset = query.Offset
		result.Names = append([]string{}, selected[query.Offset:end]...)
		result.EffectiveLimit = len(result.Names)
		result.NextOffset = end
		result.HasMore = end < len(selected)
		// Preserve complete JSON and return a continuation instead of clipping.
		for len(result.Names) > 0 {
			encoded, _ := json.Marshal(result)
			if len(encoded) <= 5500 {
				break
			}
			result.Names = result.Names[:len(result.Names)-1]
			result.EffectiveLimit = len(result.Names)
			result.NextOffset = query.Offset + len(result.Names)
			result.HasMore = result.NextOffset < len(selected)
			result.LimitReason = "response_budget"
		}
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}
