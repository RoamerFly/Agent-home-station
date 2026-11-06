package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	douyinapp "wechat-bot/connected-apps/douyin/adapter"
	"wechat-bot/internal/apphost"

	"wechat-bot/internal/websession"
)

func (h *localHomeService) douyinRoutes(s *websession.Store, mux *http.ServeMux, registry apphost.Registry, serviceContext context.Context) {
	entry, ok := registry.Lookup(douyinapp.ID)
	douyin, supported := entry.Provider.(*douyinapp.SearchApp)
	if !ok || !supported {
		return
	}
	handler := s.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		record, err := h.identity.localIdentity()
		sessionID, _, authenticated := s.Lookup(r)
		if err != nil || record.LocalOwner == nil || !authenticated {
			fail(w, http.StatusConflict, "请先准备本机主人身份并登录")
			return
		}
		var result any
		if r.Method == http.MethodGet {
			result, err = douyin.AccountAuthorization()
		} else {
			r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
			var input douyinapp.AccountAuthorizationInput
			if !decode(w, r, &input) {
				return
			}
			binding := sha256.Sum256([]byte(record.FishID + "\x00" + record.LocalOwner.ID + "\x00" + sessionID))
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			stop := context.AfterFunc(serviceContext, cancel)
			defer stop()
			result, err = douyin.AuthorizeAccount(ctx, hex.EncodeToString(binding[:]), input)
		}
		switch {
		case errors.Is(err, douyinapp.ErrAccountInput), errors.Is(err, douyinapp.ErrAccountCallback):
			fail(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, douyinapp.ErrAccountScope):
			fail(w, http.StatusForbidden, err.Error())
		case errors.Is(err, douyinapp.ErrAccountRevision), errors.Is(err, douyinapp.ErrAccountIdentity),
			errors.Is(err, douyinapp.ErrAccountRefreshUnavailable), errors.Is(err, douyinapp.ErrAccountRefreshBusy):
			fail(w, http.StatusConflict, err.Error())
		case errors.Is(err, douyinapp.ErrAccountExchange), errors.Is(err, douyinapp.ErrAccountProfile), errors.Is(err, douyinapp.ErrAccountRefreshFailed):
			fail(w, http.StatusBadGateway, err.Error())
		case err != nil:
			fail(w, http.StatusServiceUnavailable, "抖音账号授权暂不可用，请刷新后核对状态")
		default:
			respond(w, http.StatusOK, result)
		}
	})
	mux.HandleFunc("GET /api/local/apps/douyin/authorization", handler)
	mux.HandleFunc("POST /api/local/apps/douyin/authorization", handler)
}
