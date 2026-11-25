package companion

import (
	"context"
	"encoding/json"
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

func overviewGroup(name string) string {
	parts := strings.SplitN(name, "_", 3)
	if len(parts) > 1 && (parts[0] == "station" || parts[0] == "agent") {
		return parts[0] + "_" + parts[1]
	}
	return parts[0]
}

func (e *AgentRunner) toolOverview(ctx context.Context, granted []mcptools.Tool) (string, error) {
	groups := map[string][]string{}
	for _, tool := range granted {
		group := overviewGroup(tool.Name)
		groups[group] = append(groups[group], tool.Name)
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
		sort.Strings(groups[name])
	}
	sort.Strings(names)
	rows := make([]map[string]any, 0, len(names))
	for _, name := range names {
		rows = append(rows, map[string]any{"group": name, "tools": groups[name]})
	}
	result := map[string]any{
		"scope":                 "current_conversation_authorized",
		"authorized_tool_count": len(granted),
		"groups":                rows,
		"detail_tool":           "discover_tools",
		"health_probed":         false,
	}
	if provider, ok := e.Tools.(toolOverviewProvider); ok {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if host, err := provider.ToolOverview(probeCtx); err == nil {
			result["host"] = host
		} else {
			result["host_status"] = "unavailable"
		}
	}
	data, err := json.Marshal(result)
	return string(data), err
}
