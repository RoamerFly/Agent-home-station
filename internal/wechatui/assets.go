// Package wechatui carries the approved legacy WeChat UI without a second
// account runtime or the old global admin session.
package wechatui

import (
	"embed"
	"net/http"
	"path"
)

//go:embed static/*
var assets embed.FS

func Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /apps/wechat/workbench", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apps/wechat/workbench" {
			http.NotFound(w, r)
			return
		}
		serve(w, "workbench.html", "text/html; charset=utf-8")
	})
	for _, name := range []string{"bridge.js", "start.js", "workbench.js", "flow-model.js", "rounds.js", "dialogs.js", "flow-default.json", "bridge.css", "workbench.css"} {
		mux.HandleFunc("GET /assets/wechat/"+name, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/assets/wechat/"+name {
				http.NotFound(w, r)
				return
			}
			kind := "application/javascript; charset=utf-8"
			switch path.Ext(name) {
			case ".css":
				kind = "text/css; charset=utf-8"
			case ".json":
				kind = "application/json"
			}
			serve(w, name, kind)
		})
	}
}

func serve(w http.ResponseWriter, name, kind string) {
	data, err := assets.ReadFile("static/" + name)
	if err != nil {
		http.Error(w, "应用页面不可用", 503)
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' blob:; connect-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'")
	_, _ = w.Write(data)
}
