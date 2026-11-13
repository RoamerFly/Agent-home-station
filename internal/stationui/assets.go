// Package stationui owns the small, application-independent desktop home.
//
// Keeping these assets out of admin lets agent-home-station embed only its own
// first-run UI. The legacy management console continues to expose the old
// /local-home compatibility route through the same files.
package stationui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"

	"wechat-bot/internal/wechatui"
)

//go:embed static/*
var files embed.FS

type asset struct {
	file        string
	contentType string
}

var routes = map[string]asset{
	"/":                      {file: "local-home.html", contentType: "text/html; charset=utf-8"},
	"/assets/local-home.js":  {file: "local-home.js", contentType: "application/javascript; charset=utf-8"},
	"/assets/local-home.css": {file: "local-home.css", contentType: "text/css; charset=utf-8"},
	// The station shell uses the smaller WebP; the PNG stays as the legacy
	// console's fallback because that page still links to it.
	"/assets/whale-girl-cutout.webp": {file: "whale-girl-cutout.webp", contentType: "image/webp"},
	"/assets/whale-girl-cutout.png":  {file: "whale-girl-cutout.png", contentType: "image/png"},
	"/assets/night-sea.webp":         {file: "night-sea.webp", contentType: "image/webp"},
	"/assets/tide-atlas.webp":        {file: "tide-atlas.webp", contentType: "image/webp"},
}

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// Register mounts the station's public shell. It intentionally includes only
// static assets; all state-changing station endpoints remain in the host.
func Register(mux *http.ServeMux) {
	wechatui.Register(mux)
	for route := range routes {
		mux.HandleFunc("GET "+route, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != route || !serve(w, r, route) {
				http.NotFound(w, r)
			}
		})
	}
}

// ServeLegacy writes a station page or asset for a legacy compatibility route. It
// reports false when path is not part of the station UI, so callers can keep
// their own routing and 404 behavior.
func ServeLegacy(w http.ResponseWriter, r *http.Request, path string) bool {
	switch path {
	case "/":
		return false
	case "/local-home":
		path = "/"
	}
	return serve(w, r, path)
}

// assetDigests is computed once because the embedded files never change while
// the process runs. It lets the browser revalidate instead of refetching the
// whole shell on every window reload.
var assetDigests = func() map[string]string {
	digests := make(map[string]string, len(routes))
	for route, entry := range routes {
		data, err := files.ReadFile("static/" + entry.file)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		digests[route] = `"` + hex.EncodeToString(sum[:12]) + `"`
	}
	return digests
}()

func serve(w http.ResponseWriter, r *http.Request, path string) bool {
	entry, ok := routes[path]
	if !ok {
		return false
	}
	data, err := files.ReadFile("static/" + entry.file)
	if err != nil {
		return false
	}
	etag := assetDigests[path]
	w.Header().Set("Content-Type", entry.contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	if entry.contentType != "text/html; charset=utf-8" {
		// Assets are revalidated with a strong ETag instead of being forbidden
		// from the cache; the HTML shell keeps no-store so an update is picked
		// up on the next reload.
		w.Header().Set("Cache-Control", "no-cache")
		if etag != "" {
			w.Header().Set("ETag", etag)
			if r != nil && r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return true
			}
		}
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	_, _ = w.Write(data)
	return true
}
