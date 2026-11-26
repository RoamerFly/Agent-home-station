package admin

import (
	"context"
	"encoding/json"
)

// music-bridge is the legacy WeChat music service, which is deliberately not
// part of the Station package lifecycle catalogue.
func (p *permittedTools) musicBridgeStatus(ctx context.Context) map[string]any {
	health := p.probeYuE(ctx)
	status := p.manager.windowsMusicStatus()
	view := map[string]any{
		"id": "music-bridge", "state": health.State, "ready": health.Ready,
		"registered": p.manager.windowsMusicRegistered(),
		"running":    status.Running, "busy": status.Busy,
	}
	if health.BridgeVersion > 0 {
		view["bridge_version"] = health.BridgeVersion
	}
	return view
}

func (p *permittedTools) manageMusicBridge(ctx context.Context, action string) (string, error) {
	view := p.musicBridgeStatus(ctx)
	finish := func(state, reason, nextAction string, current map[string]any) (string, error) {
		result := map[string]any{"repair_state": state, "tool": current}
		if reason != "" {
			result["reason"] = reason
		}
		if nextAction != "" {
			result["next_action"] = nextAction
		}
		out, _ := json.Marshal(result)
		return string(out), nil
	}
	fail := func(reason string) (string, error) {
		return finish("failed", reason, "check_music_bridge_status_and_configuration", p.musicBridgeStatus(ctx))
	}
	if action == "status" {
		out, _ := json.Marshal(view)
		return string(out), nil
	}
	if action == "enable" {
		return finish("needs_configuration", "music_bridge_uses_registered_service", "configure_music_bridge", view)
	}
	if action == "stop" {
		if !p.privileged() {
			return finish("requires_owner", "stop_requires_owner", "ask_owner_to_stop", view)
		}
		if _, err := p.controlMusicService(ctx, "stop"); err != nil {
			return fail("music_bridge_stop_failed")
		}
		return finish("stopped", "", "", p.musicBridgeStatus(ctx))
	}
	if action == "ensure_ready" || action == "repair" {
		if ready, _ := view["ready"].(bool); ready {
			return finish("ready", "", "", view)
		}
		if registered, _ := view["registered"].(bool); !registered {
			return finish("needs_configuration", "music_bridge_not_registered", "configure_music_bridge", view)
		}
		if action == "repair" && !p.privileged() {
			return finish("requires_owner", "repair_requires_owner", "ask_owner_to_repair", view)
		}
		if running, _ := view["running"].(bool); running {
			if action == "ensure_ready" {
				return finish("unhealthy", "music_bridge_health_failed", "ask_owner_to_repair", view)
			}
			if busy, _ := view["busy"].(bool); busy {
				return finish("busy", "music_bridge_has_pending_jobs", "wait_until_idle_then_repair", view)
			}
			if _, err := p.controlMusicService(ctx, "stop"); err != nil {
				return fail("music_bridge_stop_failed")
			}
		}
		if _, err := p.controlMusicService(ctx, "start"); err != nil {
			return fail("music_bridge_start_failed")
		}
		result := p.musicBridgeStatus(ctx)
		if result["ready"] == true {
			return finish("ready", "", "", result)
		}
		return finish("unhealthy", "music_bridge_health_failed", "check_music_bridge_configuration", result)
	}
	if action == "restart" {
		if !p.privileged() {
			return finish("requires_owner", "restart_requires_owner", "ask_owner_to_restart", view)
		}
		if busy, _ := view["busy"].(bool); busy {
			return finish("busy", "music_bridge_has_pending_jobs", "wait_until_idle_then_restart", view)
		}
		if running, _ := view["running"].(bool); running {
			if _, err := p.controlMusicService(ctx, "stop"); err != nil {
				return fail("music_bridge_stop_failed")
			}
		}
	}
	if _, err := p.controlMusicService(ctx, "start"); err != nil {
		return fail("music_bridge_start_failed")
	}
	result := p.musicBridgeStatus(ctx)
	if result["ready"] != true {
		return finish("unhealthy", "music_bridge_health_failed", "check_music_bridge_configuration", result)
	}
	return finish("ready", "", "", result)
}
