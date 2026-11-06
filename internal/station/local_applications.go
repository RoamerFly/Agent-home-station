package station

import (
	"net/http"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/stationhost"
	"wechat-bot/internal/websession"
)

// attachApplications prepares the verified providers before the station host
// publishes their routes or starts background restoration.
func (h *localChatService) attachApplications(registry apphost.Registry) (apphost.Registry, []stationhost.Attachment, error) {
	registry, err := h.home.bindBundledObservationApps(registry)
	if err != nil {
		return apphost.Registry{}, nil, err
	}
	registry, h.wechat, err = bindStationWechat(registry, h.home.dataDir, h.home.root, h.models.applicationModel(), h.home.artifacts)
	if err != nil {
		return apphost.Registry{}, nil, err
	}
	apps := []stationhost.Attachment{{Mount: func(owner *websession.Store, mux *http.ServeMux) {
		h.home.bilibiliRoutes(owner, mux, registry)
		h.home.douyinRoutes(owner, mux, registry, h.ctx)
	}}}
	if h.wechat != nil {
		apps = append(apps, h.wechat.attachment(h.home.applicationOwner))
	}
	return registry, apps, nil
}
