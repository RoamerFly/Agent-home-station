package admin

import (
	"context"
	"encoding/json"

	"wechat-bot/internal/pluginhost"
)

// localASRStatus reports only the currently installed media plugin's local
// profile. The former speech configuration and public manifest runner are no
// longer an execution path, so they must not make the desktop UI claim that
// local ASR is usable.
func (m *Manager) localASRStatus() map[string]any {
	state := map[string]any{
		"available": false,
		"message":   "本地语音识别尚未通过媒体理解插件登记",
	}
	ctx := context.Background()
	store, err := m.packageStore()
	if err != nil {
		state["message"] = "媒体插件存储不可用"
		return state
	}
	installed, err := store.Installed(ctx, localASRPluginID)
	if err != nil || installed.Removed || !installed.Enabled {
		state["message"] = "媒体理解插件未安装或未启用"
		return state
	}
	configuration, err := store.Configuration(ctx, localASRPluginID)
	if err != nil {
		state["message"] = "媒体理解插件配置不可读取"
		return state
	}
	var backend string
	if json.Unmarshal(configuration.Values["asr_backend"], &backend) != nil || backend != "local" {
		state["message"] = "媒体理解插件当前未选择本地语音识别"
		return state
	}
	profile, ok := m.registeredLocalASRProfile(ctx, installed.Package, pluginhost.Grant{
		Account:     m.native.Account(),
		Plugin:      installed.Package.ID,
		Version:     installed.Package.Version,
		Permissions: installed.Permissions,
	})
	if !ok {
		state["message"] = "本地语音识别资源尚未登记或已失效"
		return state
	}
	state["available"] = true
	state["id"] = profile.initializer.ProfileID
	state["resource_version"] = profile.initializer.ResourceVersion
	state["message"] = "本地语音识别资源已登记；实际识别仍会核验来源、授权和输入音频"
	return state
}
