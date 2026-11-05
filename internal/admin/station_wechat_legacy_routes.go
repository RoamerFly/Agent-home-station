package admin

import (
	"encoding/hex"
	"errors"
	"net/http"

	"wechat-bot/internal/apphost"
)

func (s *stationWechat) legacyImportRoutes(server *Server, mux *http.ServeMux) {
	const path = "/api/apps/wechat/connections/{connection}/legacy-import"
	mux.HandleFunc("GET "+path, server.protected(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id := r.PathValue("connection")
		prior, found, err := s.savedLegacyImport(r.Context(), id, true)
		if err != nil {
			legacyImportFailure(w, err)
			return
		}
		if found {
			respond(w, http.StatusOK, prior)
			return
		}
		target, err := s.worker(id, false)
		if err != nil {
			legacyImportFailure(w, err)
			return
		}
		if target != nil {
			legacyImportFailure(w, errLegacyImportConflict)
			return
		}
		archive, err := s.previewLegacyImport(r.Context(), id)
		if err != nil {
			legacyImportFailure(w, err)
			return
		}
		respond(w, http.StatusOK, archive.Plan)
	}))
	mux.HandleFunc("POST "+path, server.protected(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var input struct {
			Digest string `json:"digest"`
		}
		if !decode(w, r, &input) {
			return
		}
		digest, err := hex.DecodeString(input.Digest)
		if err != nil || len(digest) != 32 {
			fail(w, http.StatusBadRequest, "请提交旧版预演的摘要")
			return
		}
		result, err := s.applyLegacyImport(r.Context(), r.PathValue("connection"), input.Digest)
		if err != nil {
			legacyImportFailure(w, err)
			return
		}
		respond(w, http.StatusOK, result)
	}))
}

func legacyImportFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apphost.ErrNotFound):
		fail(w, http.StatusNotFound, "找不到匹配的在册账号")
	case errors.Is(err, errLegacyImportNotConfigured):
		fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errLegacyImportInvalid):
		fail(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, errLegacyImportChanged), errors.Is(err, errLegacyImportConflict):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, apphost.ErrLifecycleConflict):
		fail(w, http.StatusConflict, "成长站正在停止，稍后可重新预演")
	default:
		fail(w, http.StatusServiceUnavailable, "暂时无法核对或保存旧版接入记录")
	}
}
