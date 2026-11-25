package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"time"
)

// ToolOverview adds a compact host snapshot to the Agent's authorized tool
// catalogue. It reports registration and supervisor state without starting a
// service or treating an unprobed backend as healthy.
func (p *permittedTools) ToolOverview(ctx context.Context) (map[string]any, error) {
	if !p.agentToolsAllowed() {
		return nil, nil
	}
	overview := map[string]any{}
	if runtime.GOOS == "windows" && p.manager.windowsMusicRegistered() {
		status := p.manager.windowsMusicStatus()
		bridge := map[string]any{
			"registered":         true,
			"supervisor_running": status.Running,
			"backend_probed":     false,
		}
		if p.privileged() || p.workspaceExecutionAllowed() {
			bridge["start_tool"] = "station_tool_manage"
			bridge["start_action"] = "ensure_ready"
			bridge["tool_id"] = "music-bridge"
		}
		overview["music_bridge"] = bridge
	}
	if p.manager.agent != nil {
		host := p.manager.agent.config().Host
		overview["workspace_execution"] = map[string]any{
			"mode":                   host.Mode,
			"gpu_mounted_by_default": host.Isolation != nil && host.Isolation.GPU,
			"host_gpu_probed":        false,
		}
	}
	if !p.workspaceExecutionAllowed() {
		return overview, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	session, err := p.stationToolLogin(probeCtx)
	if err != nil {
		overview["station_catalog"] = "unavailable"
		return overview, nil
	}
	data, err := p.stationToolRequest(probeCtx, session, http.MethodGet, "/api/local/project-tools", nil)
	if err != nil {
		overview["station_catalog"] = "unavailable"
		return overview, nil
	}
	var view struct {
		Tools []stationToolView `json:"tools"`
	}
	if json.Unmarshal(data, &view) != nil {
		overview["station_catalog"] = "invalid"
		return overview, nil
	}
	services := make([]map[string]any, 0, len(view.Tools))
	for _, tool := range view.Tools {
		item := map[string]any{
			"id": tool.ID, "installed": tool.Installed,
			"enabled": tool.Enabled, "state": tool.State,
			"runtime_ready": tool.RuntimeReady,
		}
		if tool.ID == "external-tools" {
			backends := make([]map[string]string, 0, len(tool.Backends))
			for _, backend := range tool.Backends {
				backends = append(backends, map[string]string{"name": backend.Name, "state": backend.State})
			}
			item["backends"] = backends
		}
		services = append(services, item)
	}
	overview["station_catalog"] = services
	return overview, nil
}
