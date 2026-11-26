package admin

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Repair a known missing dependency before enqueueing any generation. This
// never runs after a submission whose outcome may be unknown.
func (p *permittedTools) recoverMusicDependency(ctx context.Context) (bool, error) {
	if runtime.GOOS != "windows" || (!p.privileged() && !p.workspaceExecutionAllowed()) || !p.manager.windowsMusicRegistered() {
		return false, nil
	}
	_, err := p.controlMusicService(ctx, "start")
	return err == nil, err
}

// A known stopped Windows supervisor can be started before probing its HTTP
// bridge. Otherwise a short health deadline may expire in the plugin runtime
// before the bridge reports a transport-specific error to the caller.
func (p *permittedTools) ensureMusicDependency(ctx context.Context) error {
	if runtime.GOOS != "windows" || p.manager.windowsMusicStatus().Running {
		return nil
	}
	_, err := p.recoverMusicDependency(ctx)
	return err
}

// Called only by the installed capability's bound host-service callback.
func (p *permittedTools) startMusicServiceHost(ctx context.Context) (string, error) {
	authority, _ := ctx.Value(speechAdminTestContextKey{}).(*speechAdminTestAuthority)
	if authority != nil {
		if err := authority.valid(ctx, p.manager); err != nil {
			return "", err
		}
	} else if !p.privileged() && !p.workspaceExecutionAllowed() {
		return "", toolInputError("music_service_start_requires_owner")
	}
	if runtime.GOOS == "windows" {
		return p.manager.startWindowsMusic(ctx)
	}
	endpoint, _, err := p.manager.localMusicPluginConnection(ctx)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(endpoint)
	if err != nil || runtime.GOOS != "linux" || u.Scheme != "http" || u.Host != "127.0.0.1:6188" || u.Path != "" {
		return "", toolInputError("managed_local_music_unit_not_applicable")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	unit := filepath.Join(home, ".config/systemd/user/wechat-agent-music.service")
	info, err := os.Lstat(unit)
	if err != nil || !info.Mode().IsRegular() {
		return "", toolInputError("managed_music_unit_not_installed")
	}
	if err := checkMusicServiceControl(ctx); err != nil {
		return "", err
	}
	if err := exec.CommandContext(ctx, "systemctl", "--user", "start", "--no-block", "wechat-agent-music.service").Run(); err != nil {
		return "", toolInputError("managed_music_start_failed_check_diagnostics")
	}
	b, _ := json.Marshal(map[string]any{"state": "starting_or_active", "unit": "wechat-agent-music.service", "instruction": "已请求启动已有本机音乐服务。用music_service_status核对bridge_ready与backend_ready，再提交或续办原任务；服务启动不代表换声完成，不更改声线、不创建歌曲或发送。"})
	return string(b), nil
}
