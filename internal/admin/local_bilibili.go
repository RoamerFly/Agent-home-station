package admin

import (
	"errors"
	"net/http"
	"path/filepath"

	biliapp "wechat-bot/connected-apps/bilibili/adapter"
	"wechat-bot/internal/apphost"
)

func (h *localHomeService) bindBundledBilibili(registry apphost.Registry) (apphost.Registry, error) {
	if _, registered := registry.Lookup(biliapp.ID); registered {
		return registry, nil
	}
	if _, _, err := h.verifyAppSource(biliapp.ID); err != nil {
		return registry, nil
	}
	bili := biliapp.New(filepath.Join(h.dataDir, "apps", biliapp.ID))
	version, transcribe := h.localBilibiliAudioCallbacks()
	bili.SetAudioTranscriber(version, transcribe)
	entries := make([]apphost.Registration, 0, len(registry.Applications())+1)
	for _, app := range registry.Applications() {
		entry, _ := registry.Lookup(app.ID)
		entries = append(entries, entry)
	}
	return apphost.New(append(entries, bili.Registration())...)
}

func (h *localHomeService) bilibiliRoutes(s *Server, mux *http.ServeMux, registry apphost.Registry) {
	entry, ok := registry.Lookup(biliapp.ID)
	bili, supported := entry.Provider.(*biliapp.PublicApp)
	if !ok || !supported {
		return
	}
	handler := s.protected(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		record, err := h.identity.localIdentity()
		if err != nil || record.LocalOwner == nil {
			fail(w, http.StatusConflict, "请先准备本机主人身份")
			return
		}
		var view biliapp.Settings
		if r.Method == http.MethodGet {
			view, err = bili.View()
		} else {
			var in struct {
				Revision        *uint64  `json:"revision"`
				Enabled         *bool    `json:"enabled"`
				Videos          []string `json:"videos"`
				BrowserEnabled  bool     `json:"browser_enabled"`
				PlaybackEnabled bool     `json:"playback_enabled"`
				CommentsEnabled bool     `json:"comments_enabled"`
				AudioEnabled    bool     `json:"audio_enabled"`
			}
			if !decode(w, r, &in) {
				return
			}
			if in.Revision == nil || in.Enabled == nil || in.Videos == nil {
				fail(w, http.StatusBadRequest, "请提交读取开关、公开 BV 号列表和当前设置版本")
				return
			}
			if in.BrowserEnabled && !*in.Enabled {
				fail(w, http.StatusBadRequest, "启用独立浏览器前，请先开启公开读取并填写 BV 号")
				return
			}
			if in.PlaybackEnabled && !in.BrowserEnabled {
				fail(w, http.StatusBadRequest, "启用播放采样前，请先开启独立浏览器")
				return
			}
			if in.AudioEnabled && !*in.Enabled {
				fail(w, http.StatusBadRequest, "启用音轨读取前，请先开启公开读取并填写 BV 号")
				return
			}
			if in.CommentsEnabled && !*in.Enabled {
				fail(w, http.StatusBadRequest, "启用评论读取前，请先开启公开读取并填写 BV 号")
				return
			}
			view, err = bili.Save(*in.Revision, biliapp.Settings{
				Enabled: *in.Enabled, Videos: in.Videos, BrowserEnabled: in.BrowserEnabled,
				PlaybackEnabled: in.PlaybackEnabled, CommentsEnabled: in.CommentsEnabled, AudioEnabled: in.AudioEnabled,
			})
		}
		switch {
		case errors.Is(err, biliapp.ErrConflict):
			fail(w, http.StatusConflict, "B 站读取设置已变化，请重新读取后保存")
		case errors.Is(err, biliapp.ErrInvalid):
			fail(w, http.StatusBadRequest, "请提供有效的 BV 号，最多 100 条且不重复")
		case errors.Is(err, apphost.ErrObservationBrowserUnavailable):
			fail(w, http.StatusServiceUnavailable, apphost.ErrObservationBrowserUnavailable.Error())
		case err != nil:
			fail(w, http.StatusServiceUnavailable, "B 站读取设置暂不可用，已保留原配置")
		default:
			respond(w, http.StatusOK, struct {
				biliapp.Settings
				BrowserAvailable bool `json:"browser_available"`
				AudioAvailable   bool `json:"audio_available"`
			}{view, bili.BrowserAvailable(), bili.AudioAvailable(r.Context())})
		}
	})
	mux.HandleFunc("GET /api/local/apps/bilibili/public", handler)
	mux.HandleFunc("PUT /api/local/apps/bilibili/public", handler)
}
