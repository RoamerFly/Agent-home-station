package admin

import (
	"errors"
	"net/http"
	"strings"

	"wechat-bot/internal/apphost"
)

func (s *Server) appRoutes(mux *http.ServeMux, registry apphost.Registry) {
	mux.HandleFunc("GET /api/apps", s.protected(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		respond(w, http.StatusOK, map[string]any{"items": registry.Applications()})
	}))
	handle := func(pattern string, query func(apphost.Registration, *http.Request) (any, error)) {
		mux.HandleFunc("GET "+pattern, s.protected(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			app, ok := registry.Lookup(r.PathValue("app"))
			if !ok {
				fail(w, http.StatusNotFound, "应用未登记")
				return
			}
			result, err := query(app, r)
			switch {
			case errors.Is(err, apphost.ErrNotFound):
				fail(w, http.StatusNotFound, "应用连接不存在")
			case err != nil:
				fail(w, http.StatusServiceUnavailable, "应用登记暂不可用")
			default:
				respond(w, http.StatusOK, result)
			}
		}))
	}
	handle("/api/apps/{app}/connections", func(app apphost.Registration, r *http.Request) (any, error) {
		items, err := app.Provider.Connections(r.Context())
		return map[string]any{"application": app.Application, "items": items}, err
	})
	handle("/api/apps/{app}/connections/{connection}", func(app apphost.Registration, r *http.Request) (any, error) {
		status, err := app.Provider.Inspect(r.Context(), r.PathValue("connection"))
		return map[string]any{"application": app.Application, "connection": status}, err
	})
}

func isAppRegistryPath(path string) bool {
	return path == "/api/apps" || strings.HasPrefix(path, "/api/apps/")
}
