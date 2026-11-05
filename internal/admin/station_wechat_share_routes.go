package admin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/sharespec"
	catalog "wechat-bot/plugins"
)

func (s *stationWechat) shareRoutes(owner *Server, mux *http.ServeMux) {
	for _, plugin := range []string{shareServicePluginID, localASRPluginID} {
		path := "/api/apps/wechat/connections/{connection}/" + plugin
		handler := owner.protected(func(w http.ResponseWriter, r *http.Request) {
			s.stationMediaSettings(w, r, plugin)
		})
		mux.HandleFunc("GET "+path, handler)
		mux.HandleFunc("PUT "+path, handler)
	}
}

func (s *stationWechat) stationMediaSettings(w http.ResponseWriter, r *http.Request, plugin string) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, id := r.Context(), r.PathValue("connection")
	if !validID(id) {
		fail(w, 400, "连接编号无效")
		return
	}
	native, err := s.WorkerSender(ctx, id)
	if err != nil {
		fail(w, 404, "微信连接不可用")
		return
	}
	var input struct {
		Action       string                  `json:"action"`
		Revision     int                     `json:"revision"`
		GrantNetwork bool                    `json:"grant_network"`
		GrantMedia   bool                    `json:"grant_media"`
		Config       pluginhost.ConfigUpdate `json:"configuration"`
	}
	if r.Method == http.MethodPut && !decode(w, r, &input) {
		return
	}
	worker, err := s.worker(id, r.Method == http.MethodPut)
	if err != nil {
		fail(w, 503, "工具宿主不可用")
		return
	}
	view := map[string]any{"installed": false, "enabled": false, "runtime_ready": stationShareRuntimeReady(s.shareOptions)}
	if worker == nil {
		respond(w, 200, view)
		return
	}
	host, err := s.shareHost(worker, native.Account())
	if err != nil {
		fail(w, 503, "工具宿主不可用")
		return
	}
	store := host.store
	item, err := store.Installed(ctx, plugin)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, 503, "工具安装状态不可读")
		return
	}
	if r.Method == http.MethodPut {
		if input.Action == "install" {
			permissions := []string{"network"}
			approved := input.GrantNetwork
			if plugin == localASRPluginID {
				permissions = []string{"artifact.read", "artifact.write", "model.invoke"}
				approved = input.GrantMedia
			}
			if !approved {
				fail(w, 400, "安装需要明确同意工具权限")
				return
			}
			contract, e := catalog.ContractFor(plugin)
			if e != nil {
				fail(w, 503, "官方工具声明不可读")
				return
			}
			if errors.Is(err, sql.ErrNoRows) || item.Removed {
				prepared, e := store.Prepare(ctx, filepath.Join(s.shareOptions.CatalogDirectory, plugin))
				if e != nil || prepared.Digest != contract.ManifestSHA256 || prepared.Package.ID != plugin {
					fail(w, 409, "工具发行源缺失或摘要不匹配")
					return
				}
				item, err = store.Install(ctx, prepared.Token, prepared.Digest, permissions)
			} else if item.Digest != contract.ManifestSHA256 {
				fail(w, 409, "已安装版本需要单独升级")
				return
			}
		} else {
			if err != nil || item.Removed || input.Revision != item.Revision {
				fail(w, 409, "工具版本已变化，请刷新后重试")
				return
			}
			switch input.Action {
			case "configure":
				if raw, exists := input.Config.Values["object_bridge_url"]; plugin == shareServicePluginID && exists {
					var addr string
					if json.Unmarshal(raw, &addr) != nil {
						fail(w, 400, "对象桥接地址无效")
						return
					}
					if _, e := sharespec.ObjectBridgeURL(addr); e != nil {
						fail(w, 400, "对象桥接只接受本机环回地址")
						return
					}
				}
				if plugin == localASRPluginID && !stationMediaConfigSupported(input.Config) {
					fail(w, 400, "当前连接支持远程视觉与远程语音识别配置")
					return
				}
				_, err = store.SaveConfiguration(ctx, plugin, input.Config)
			case "enable":
				err = host.controller.EnableOnDemand(ctx, plugin, input.Revision)
			case "disable":
				err = host.controller.Stop(ctx, plugin, input.Revision)
			default:
				fail(w, 400, "未知的工具设置操作")
				return
			}
		}
		if err != nil {
			fail(w, 409, "设置未保存，请检查工具是否已停用、任务是否仍在运行以及配置版本")
			return
		}
		item, err = store.Installed(ctx, plugin)
	}
	if errors.Is(err, sql.ErrNoRows) || item.Removed {
		respond(w, 200, view)
		return
	}
	if err != nil {
		fail(w, 503, "工具安装状态不可读")
		return
	}
	configuration, err := store.Configuration(ctx, plugin)
	if err != nil {
		fail(w, 503, "工具配置状态不可读")
		return
	}
	view["installed"], view["enabled"], view["revision"], view["version"] = true, item.Enabled, item.Revision, item.Package.Version
	view["configuration"] = configuration
	respond(w, 200, view)
}

func stationMediaConfigSupported(config pluginhost.ConfigUpdate) bool {
	for key := range config.Values {
		switch key {
		case "vision_endpoint", "vision_model", "vision_backend", "thinking", "reasoning_effort", "max_tokens", "asr_endpoint", "asr_model":
		default:
			return false
		}
	}
	for key := range config.Secrets {
		if key != "vision_key" && key != "asr_key" {
			return false
		}
	}
	for _, key := range config.ClearSecrets {
		if key != "vision_key" && key != "asr_key" {
			return false
		}
	}
	return true
}
