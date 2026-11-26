package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

// WeChat service calls execute from the account's installed package snapshot,
// which can differ from the Station's package with the same directory ID.
type wechatToolView struct {
	ExecutionHost string                 `json:"execution_host"`
	ID            string                 `json:"id"`
	Name          string                 `json:"name"`
	Version       string                 `json:"version"`
	Digest        string                 `json:"digest"`
	Installed     bool                   `json:"installed"`
	Enabled       bool                   `json:"enabled"`
	Revision      int                    `json:"revision"`
	State         string                 `json:"state"`
	ActiveCalls   int                    `json:"active_calls"`
	RuntimeReady  bool                   `json:"runtime_ready"`
	Readiness     pluginPackageReadiness `json:"readiness"`
	PendingJobs   bool                   `json:"pending_jobs"`
	ErrorCode     string                 `json:"error_code,omitempty"`
	Capabilities  []string               `json:"capabilities,omitempty"`
}

func compactWechatToolStatus(views []wechatToolView) []map[string]any {
	tools := make([]map[string]any, 0, len(views))
	for _, view := range views {
		tools = append(tools, map[string]any{
			"execution_host": "wechat", "id": view.ID, "name": view.Name,
			"version": view.Version, "enabled": view.Enabled, "state": view.State,
			"runtime_ready": view.RuntimeReady, "readiness": view.Readiness.State,
			"active_calls": view.ActiveCalls, "pending_jobs": view.PendingJobs,
			"error_code": view.ErrorCode,
		})
	}
	return tools
}

func (p *permittedTools) wechatToolRuntime() (*pluginhost.Store, *pluginruntime.Controller, bool, error) {
	store, err := p.manager.packageStore()
	if err != nil {
		return nil, nil, false, err
	}
	h, ready, err := p.manager.installedPluginRuntime()
	return store, h, ready, err
}

func (p *permittedTools) wechatToolInstalled(ctx context.Context, id string) (bool, error) {
	store, err := p.manager.packageStore()
	if err != nil {
		return false, err
	}
	item, err := store.Installed(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && !item.Removed, err
}

func wechatToolStatus(ctx context.Context, store *pluginhost.Store, h *pluginruntime.Controller, ready bool, item pluginhost.InstalledPackage, detail bool) (wechatToolView, error) {
	view := wechatToolView{
		ExecutionHost: "wechat", ID: item.Package.ID, Name: item.Package.Name,
		Version: item.Package.Version, Digest: item.Digest, Installed: true,
		Enabled: item.Enabled, Revision: item.Revision, State: "stopped", RuntimeReady: ready,
		Readiness: pluginReadiness(ctx, store, h, item, ready),
	}
	if h != nil {
		status := h.Status(item.Package.ID)
		view.State, view.ActiveCalls, view.ErrorCode = status.State, status.ActiveCalls, status.ErrorCode
	}
	pending, err := store.HasUnfinishedJobs(ctx, item.Package.ID)
	if err != nil {
		return view, err
	}
	view.PendingJobs = pending
	if detail {
		caps, err := store.Capabilities(ctx, item.Package.ID)
		if err != nil {
			return view, err
		}
		for _, cap := range caps {
			if cap.Discoverable && cap.Implementation == "ready" {
				view.Capabilities = append(view.Capabilities, cap.ID)
			}
		}
	}
	return view, nil
}

func (p *permittedTools) wechatToolList(ctx context.Context) ([]wechatToolView, error) {
	store, h, ready, err := p.wechatToolRuntime()
	if err != nil {
		return nil, err
	}
	items, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]wechatToolView, 0, len(items))
	for _, item := range items {
		if item.Removed {
			continue
		}
		view, err := wechatToolStatus(ctx, store, h, ready, item, false)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (p *permittedTools) manageWechatTool(ctx context.Context, action, id string) (string, error) {
	store, h, ready, err := p.wechatToolRuntime()
	if err != nil {
		return "", err
	}
	item, err := store.Installed(ctx, id)
	if err != nil || item.Removed {
		return "", errors.New("wechat_tool_not_installed")
	}
	if action != "status" && h == nil || action != "status" && !ready {
		return "", errors.New("wechat_tool_runtime_unavailable")
	}
	if action == "enable" && !p.privileged() {
		return "", errors.New("wechat_tool_owner_enable_required")
	}
	if (action == "start" || action == "restart" || action == "ensure_ready" || action == "repair") && !item.Enabled {
		return "", errors.New("wechat_tool_disabled")
	}
	switch action {
	case "status":
	case "enable":
		err = h.EnableOnDemand(ctx, id, item.Revision)
	case "start", "ensure_ready":
		err = h.EnsureStarted(ctx, id, item.Revision)
	case "stop":
		err = h.StopIdleSession(ctx, id, item.Revision)
	case "restart", "repair":
		if h.Status(id).State != "stopped" {
			err = h.StopIdleSession(ctx, id, item.Revision)
		}
		if err == nil {
			err = h.EnsureStarted(ctx, id, item.Revision)
		}
	}
	if err != nil {
		return "", err
	}
	item, err = store.Installed(ctx, id)
	if err != nil {
		return "", err
	}
	view, err := wechatToolStatus(ctx, store, h, ready, item, true)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(view)
	return string(out), nil
}
