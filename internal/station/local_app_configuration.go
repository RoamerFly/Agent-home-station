package station

import (
	"net/http"

	appcatalog "wechat-bot/connected-apps"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/websession"
)

type appConfigurationSection struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func applicationConfigurationRoutes(owner *websession.Store, mux *http.ServeMux, registry apphost.Registry) {
	mux.HandleFunc("GET /api/local/apps/{app}/configuration", owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id := r.PathValue("app")
		entry, registered := registry.Lookup(id)
		name := entry.Name
		if !registered {
			contract, err := appcatalog.Lookup(id)
			if err != nil {
				websession.Fail(w, http.StatusNotFound, "应用未登记")
				return
			}
			name = contract.Package.Name
		}

		sections := []appConfigurationSection{{"overview", "概览"}, {"account", "账号与授权"}}
		switch id {
		case "wechat":
			if registered && entry.Registration == "station-native" {
				sections = append(sections, appConfigurationSection{"flow", "流程与状态"}, appConfigurationSection{"rounds", "执行回合"})
			}
			sections = append(sections, appConfigurationSection{"conversations", "会话与记忆"}, appConfigurationSection{"surf", "网上冲浪"}, appConfigurationSection{"delivery", "投递与回执"})
			sections = append(sections, appConfigurationSection{"tasks", "任务与文档"})
		case "douyin":
			sections = append(sections, appConfigurationSection{"browse", "内容浏览"}, appConfigurationSection{"publications", "作品发布"}, appConfigurationSection{"interactions", "互动任务"}, appConfigurationSection{"live", "直播与本机预览"})
		case "bilibili":
			sections = append(sections, appConfigurationSection{"browse", "公开读取"}, appConfigurationSection{"publications", "作品发布"})
		}
		sections = append(sections, appConfigurationSection{"source", "资料与版本"})
		websession.Respond(w, http.StatusOK, map[string]any{
			"id": id, "name": name, "registered": registered, "sections": sections,
		})
	}))
}
