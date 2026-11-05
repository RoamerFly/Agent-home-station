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
	const path = "/api/apps/wechat/connections/{connection}/share-reader"
	mux.HandleFunc("GET "+path, owner.protected(s.shareSettings))
	mux.HandleFunc("PUT "+path, owner.protected(s.shareSettings))
}

func (s *stationWechat) shareSettings(w http.ResponseWriter, r *http.Request) {
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
	item, err := store.Installed(ctx, shareServicePluginID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, 503, "工具安装状态不可读")
		return
	}
	if r.Method == http.MethodPut {
		if input.Action == "install" {
			if !input.GrantNetwork {
				fail(w, 400, "安装需要明确授予受控网络访问")
				return
			}
			contract, e := catalog.ContractFor(shareServicePluginID)
			if e != nil {
				fail(w, 503, "官方解析工具声明不可读")
				return
			}
			if errors.Is(err, sql.ErrNoRows) || item.Removed {
				prepared, e := store.Prepare(ctx, filepath.Join(s.shareOptions.CatalogDirectory, shareServicePluginID))
				if e != nil || prepared.Digest != contract.ManifestSHA256 || prepared.Package.ID != shareServicePluginID {
					fail(w, 409, "解析工具发行源缺失或摘要不匹配")
					return
				}
				item, err = store.Install(ctx, prepared.Token, prepared.Digest, []string{"network"})
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
				if raw, exists := input.Config.Values["object_bridge_url"]; exists {
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
				_, err = store.SaveConfiguration(ctx, shareServicePluginID, input.Config)
			case "enable":
				err = host.controller.EnableOnDemand(ctx, shareServicePluginID, input.Revision)
			case "disable":
				err = host.controller.Stop(ctx, shareServicePluginID, input.Revision)
			default:
				fail(w, 400, "未知的解析工具设置操作")
				return
			}
		}
		if err != nil {
			fail(w, 409, "设置未保存，请检查工具是否已停用、任务是否仍在运行以及配置版本")
			return
		}
		item, err = store.Installed(ctx, shareServicePluginID)
	}
	if errors.Is(err, sql.ErrNoRows) || item.Removed {
		respond(w, 200, view)
		return
	}
	if err != nil {
		fail(w, 503, "工具安装状态不可读")
		return
	}
	configuration, err := store.Configuration(ctx, shareServicePluginID)
	if err != nil {
		fail(w, 503, "工具配置状态不可读")
		return
	}
	view["installed"], view["enabled"], view["revision"], view["version"] = true, item.Enabled, item.Revision, item.Package.Version
	view["configuration"] = configuration
	respond(w, 200, view)
}
