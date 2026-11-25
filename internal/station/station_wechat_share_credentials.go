package station

import (
	"net/http"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/sharespec"
	"wechat-bot/internal/websession"
)

// shareReaderCredentialsInput is deliberately host-facing.  The value is
// posted by the desktop process after its restricted WebView2 has collected
// cookies; it is never returned to browser JavaScript or a plugin process.
type shareReaderCredentialsInput struct {
	Provider string `json:"provider"`
	Cookie   string `json:"cookie"`
	Revision int    `json:"revision"`
}

// shareReaderCredentials stores the two share-reader provider cookies as
// pluginhost host-only secrets.  A running plugin is drained for the atomic
// configuration update and its enabled intent is restored afterwards.  The
// response contains only redacted state and never the cookie value.
func (s *stationWechat) shareReaderCredentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	connection := r.PathValue("connection")
	if !identity.ValidPathID(connection) {
		websession.Fail(w, http.StatusBadRequest, "连接编号无效")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 20<<10)
	var in shareReaderCredentialsInput
	if !websession.Decode(w, r, &in) {
		return
	}
	field := map[string]string{"yuanbao": "yuanbao_cookie", "assistant": "assistant_cookie"}[strings.ToLower(strings.TrimSpace(in.Provider))]
	if field == "" || !sharespec.ValidCookie(in.Cookie) {
		websession.Fail(w, http.StatusBadRequest, "登录凭据无效，请在对应平台完成登录后重试")
		return
	}
	native, err := s.WorkerSender(r.Context(), connection)
	if err != nil {
		websession.Fail(w, http.StatusNotFound, "微信连接不可用")
		return
	}
	worker, err := s.worker(connection, true)
	if err != nil || worker == nil {
		websession.Fail(w, http.StatusServiceUnavailable, "工具宿主不可用")
		return
	}
	host, err := s.shareHost(worker, native.Account())
	if err != nil {
		websession.Fail(w, http.StatusServiceUnavailable, "工具宿主不可用")
		return
	}
	item, err := host.store.Installed(r.Context(), wechatapp.ShareReaderToolID)
	if err != nil || item.Removed {
		websession.Fail(w, http.StatusConflict, "请先安装分享解析工具")
		return
	}
	if in.Revision != item.Revision {
		websession.Fail(w, http.StatusConflict, "工具版本已变化，请刷新后重试")
		return
	}
	configuration, err := host.store.Configuration(r.Context(), wechatapp.ShareReaderToolID)
	if err != nil {
		websession.Fail(w, http.StatusServiceUnavailable, "工具配置状态不可读")
		return
	}
	wasEnabled := item.Enabled
	if wasEnabled {
		if err = host.controller.Stop(r.Context(), wechatapp.ShareReaderToolID, item.Revision); err != nil {
			websession.Fail(w, http.StatusConflict, "分享解析工具正在处理任务，请稍后再保存登录")
			return
		}
	}
	// Preserve public settings such as object_bridge_url while replacing one
	// provider secret.  SaveConfiguration retains the other secret itself.
	_, saveErr := host.store.SaveConfiguration(r.Context(), wechatapp.ShareReaderToolID, pluginhost.ConfigUpdate{
		Revision: configuration.Revision,
		Values:   configuration.Values,
		Secrets:  map[string]string{field: in.Cookie},
	})
	if saveErr != nil {
		if wasEnabled {
			_ = host.controller.EnableOnDemand(r.Context(), wechatapp.ShareReaderToolID, item.Revision)
		}
		websession.Fail(w, http.StatusConflict, "登录凭据未保存，请刷新工具状态后重试")
		return
	}
	if wasEnabled {
		if err = host.controller.EnableOnDemand(r.Context(), wechatapp.ShareReaderToolID, item.Revision); err != nil {
			websession.Fail(w, http.StatusConflict, "登录凭据已保存，但分享解析工具未能恢复启用，请在工具卡片中重试")
			return
		}
	}
	updated, err := host.store.Configuration(r.Context(), wechatapp.ShareReaderToolID)
	if err != nil {
		websession.Fail(w, http.StatusServiceUnavailable, "工具配置状态不可读")
		return
	}
	websession.Respond(w, http.StatusOK, map[string]any{
		"provider":           strings.ToLower(strings.TrimSpace(in.Provider)),
		"configured_secrets": updated.ConfiguredSecrets,
		"enabled":            wasEnabled,
		"checked_at":         time.Now().UTC().Format(time.RFC3339Nano),
	})
}
