package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"wechat-bot/internal/linuxnative"
)

type nativeCatalog interface {
	Catalog(context.Context, string, string, string, string) (linuxnative.CatalogPage, error)
}

func (s *Server) catalogRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/catalog", s.protected(func(w http.ResponseWriter, r *http.Request) {
		native, ok := s.manager.native.(nativeCatalog)
		if !ok {
			fail(w, 501, "当前后台不支持会话目录")
			return
		}
		kind, thread := r.URL.Query().Get("kind"), r.URL.Query().Get("thread")
		if kind != "contacts" && kind != "groups" && kind != "threads" && kind != "messages" && kind != "members" {
			fail(w, 400, "目录类型无效")
			return
		}
		if kind == "messages" || kind == "members" {
			if !permittedRecipient(s.manager.Config().Config, thread) {
				fail(w, 403, "请先在回复规则中授权这个会话")
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		page, err := native.Catalog(ctx, kind, thread, r.URL.Query().Get("search"), r.URL.Query().Get("cursor"))
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				fail(w, 504, "目录查询超时，可以重新查询；群成员未同步时请刷新当前群成员")
				return
			}
			var catalogErr *linuxnative.CatalogError
			if errors.As(err, &catalogErr) {
				fail(w, 502, catalogErr.Error())
				return
			}
			fail(w, 502, "目录读取失败；请检查登录与数据同步状态")
			return
		}
		respond(w, 200, page)
	}))
}
