package station

import (
	"context"
	"errors"
)

// ensureProjectToolReady restores only an already enabled package. It never
// repeats the business request that observed the failure or changes grants.
func (s *localProjectTools) ensureProjectToolReady(ctx context.Context, host *localProjectToolHost, view localProjectToolView, repair bool) (string, error) {
	if !view.Installed {
		return "needs_installation", nil
	}
	if !view.Enabled {
		return "disabled", nil
	}
	if view.Busy {
		return "busy", nil
	}
	if !view.RuntimeReady || view.State == "not_available" {
		return "needs_configuration", nil
	}
	if view.ID == localExternalPackage && len(view.Backends) > 0 {
		missing := true
		for _, backend := range view.Backends {
			if backend.State != "missing" {
				missing = false
				break
			}
		}
		if missing {
			return "needs_configuration", nil
		}
	}
	if view.ID == localTTSPackage {
		// This package synthesizes one request at a time; it has no resident
		// service to restart after its voices have passed the setup check.
		return "ready", nil
	}
	start := func() error {
		switch view.ID {
		case localRVCPackage, localYuEPackage, localComfyPackage, localAudioCppPackage, localOBSPackage:
			return s.changeLifecycle(ctx, host, view.ID, view.Revision, "start")
		default:
			return host.controller.EnsureStarted(ctx, view.ID, view.Revision)
		}
	}
	if err := start(); err == nil {
		return "ready", nil
	} else if projectToolBusyError(err) {
		return "busy", nil
	} else if busy, checkErr := s.projectBackendBusy(ctx, host.store.Account(), view.ID); checkErr != nil {
		return "failed", err
	} else if busy {
		return "busy", nil
	} else if !repair || view.State != "running" {
		return "failed", err
	}
	// A broken resident backend may be restarted once, but never while a
	// request or unfinished job still depends on it.
	busy, err := host.store.HasUnfinishedJobs(ctx, view.ID)
	if err != nil {
		return "failed", err
	}
	if busy || s.isActive(view.ID) || host.controller.Status(view.ID).ActiveCalls > 0 {
		return "busy", nil
	}
	backendBusy, err := s.projectBackendBusy(ctx, host.store.Account(), view.ID)
	if err != nil {
		return "failed", err
	}
	if backendBusy {
		return "busy", nil
	}
	switch view.ID {
	case localRVCPackage, localYuEPackage, localComfyPackage, localAudioCppPackage, localOBSPackage:
		if err := s.changeLifecycle(ctx, host, view.ID, view.Revision, "restart", true); err != nil {
			if projectToolBusyError(err) {
				return "busy", nil
			}
			return "failed", err
		}
	default:
		if err := host.controller.StopIdleSession(ctx, view.ID, view.Revision); err != nil {
			if projectToolBusyError(err) {
				return "busy", nil
			}
			return "failed", err
		}
		if err := host.controller.EnsureStarted(ctx, view.ID, view.Revision); err != nil {
			return "failed", err
		}
	}
	if ctx.Err() != nil {
		return "failed", ctx.Err()
	}
	return "restarted", nil
}

func projectToolBusyError(err error) bool {
	if err == nil {
		return false
	}
	switch err.Error() {
	case "managed_service_owner_busy", "tool_busy_wait_for_completion", "工具正在处理任务，请完成后再停止或重启":
		return true
	default:
		return false
	}
}

// Info exposes the independent managed process's current lease state. The
// controller only tracks plugin calls, so its ActiveCalls alone is insufficient.
func (s *localProjectTools) projectBackendBusy(ctx context.Context, fish, id string) (bool, error) {
	switch id {
	case localRVCPackage:
		if s.rvc != nil {
			info, err := s.rvc.Info(ctx, fish)
			return info.Busy, err
		}
	case localYuEPackage:
		if s.yue != nil {
			info, err := s.yue.Info(ctx, fish)
			return info.Busy, err
		}
	case localComfyPackage:
		if s.comfy != nil {
			info, err := s.comfy.Info(ctx, fish)
			return info.Busy, err
		}
	case localAudioCppPackage:
		if s.audio != nil {
			info, err := s.audio.Info(ctx, fish)
			return info.Busy, err
		}
	case localOBSPackage:
		return s.obs.status(localOBSView{Enabled: true}).Active || s.obsProcesses.Status("preview").Busy, nil
	}
	return false, nil
}

func projectToolRepairResult(view localProjectToolView, state string, recoverErr error) map[string]any {
	result := map[string]any{"tool": view, "repair_state": state}
	if recoverErr != nil {
		result["repair_state"] = "failed"
		result["reason"] = "start_failed"
		var safe interface{ SafeToolFailure() (string, string) }
		if errors.As(recoverErr, &safe) {
			code, _ := safe.SafeToolFailure()
			if code != "" {
				result["reason"] = code
				switch code {
				case "plugin_dependency_missing":
					result["next_action"] = "ask_owner_to_install_dependency"
				case "plugin_configuration_required", "plugin_dependency_not_enabled", "plugin_authorization_required":
					result["next_action"] = "configure_or_enable_dependency"
				}
			}
		}
		if result["next_action"] == nil {
			result["next_action"] = "check_tool_status_and_configuration"
		}
		return result
	}
	switch state {
	case "needs_installation":
		result["reason"], result["next_action"] = "tool_not_installed", "ask_owner_to_install"
	case "disabled":
		result["reason"], result["next_action"] = "tool_disabled", "enable_tool"
	case "needs_configuration":
		reason := view.Reason
		if reason == "" {
			reason = "runtime_not_ready"
		}
		result["reason"], result["next_action"] = reason, "configure_tool"
	case "busy":
		result["reason"], result["next_action"] = "tool_has_active_work", "wait_until_idle_then_repair"
	}
	return result
}
