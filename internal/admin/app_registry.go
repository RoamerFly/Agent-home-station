package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"wechat-bot/internal/apphost"
)

var errReceiptPage = errors.New("invalid receipt page")

func (s *Server) appRoutes(mux *http.ServeMux, registry apphost.Registry) {
	s.appAccountSetupRoutes(mux, registry)
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
				fail(w, http.StatusNotFound, "未找到可读取的应用连接或记录")
			case errors.Is(err, errReceiptPage):
				fail(w, http.StatusBadRequest, "页大小须为正整数，续读位置须使用上页返回的编号")
			case err != nil:
				fail(w, http.StatusServiceUnavailable, "暂时无法核对应用记录")
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
	handle("/api/apps/{app}/connections/{connection}/events/{message}", func(app apphost.Registration, r *http.Request) (any, error) {
		reader, ok := app.Provider.(apphost.EventReader)
		if !ok {
			return nil, apphost.ErrNotFound
		}
		return reader.ReadEvent(r.Context(), r.PathValue("connection"), r.URL.Query().Get("conversation"), r.PathValue("message"))
	})
	handle("/api/apps/{app}/connections/{connection}/receipts/{key}", func(app apphost.Registration, r *http.Request) (any, error) {
		reader, ok := app.Provider.(apphost.ReceiptReader)
		if !ok {
			return nil, apphost.ErrNotFound
		}
		return reader.ReadReceipt(r.Context(), r.PathValue("connection"), r.PathValue("key"))
	})
	handle("/api/apps/{app}/connections/{connection}/receipts", func(app apphost.Registration, r *http.Request) (any, error) {
		reader, ok := app.Provider.(apphost.ReceiptLister)
		if !ok {
			return nil, apphost.ErrNotFound
		}
		limit, after := 20, r.URL.Query().Get("after")
		if value := r.URL.Query().Get("limit"); value != "" {
			var err error
			limit, err = strconv.Atoi(value)
			if err != nil || limit < 1 {
				return nil, errReceiptPage
			}
		}
		if after != "" && !validID(after) {
			return nil, errReceiptPage
		}
		return reader.ListReceipts(r.Context(), r.PathValue("connection"), after, limit)
	})
	handle("/api/apps/{app}/connections/{connection}/operations/{operation}", func(app apphost.Registration, r *http.Request) (any, error) {
		reader, ok := app.Provider.(apphost.ActionReader)
		if !ok {
			return nil, apphost.ErrNotFound
		}
		return reader.ReadAction(r.Context(), r.PathValue("connection"), r.PathValue("operation"))
	})
	mux.HandleFunc("PUT /api/apps/{app}/connections/{connection}/policy", s.protected(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		app, ok := registry.Lookup(r.PathValue("app"))
		writer, supported := app.Provider.(apphost.ConnectionPolicyWriter)
		if !ok || !supported {
			fail(w, http.StatusNotFound, "应用未提供回复范围设置")
			return
		}
		var input struct {
			Threads      *[]string `json:"reply_threads"`
			Attachments  *[]string `json:"attachment_threads"`
			MediaReplies *[]string `json:"media_reply_threads"`
		}
		if !decode(w, r, &input) {
			return
		}
		if input.Threads == nil {
			fail(w, http.StatusBadRequest, "请提供明确的回复会话列表")
			return
		}
		view, err := writer.SetConnectionPolicy(r.Context(), r.PathValue("connection"), apphost.ConnectionPolicyInput{ReplyThreads: *input.Threads, AttachmentThreads: input.Attachments, MediaReplyThreads: input.MediaReplies})
		switch {
		case errors.Is(err, apphost.ErrNotFound):
			fail(w, http.StatusNotFound, "应用连接不存在")
		case errors.Is(err, apphost.ErrInvalidConnectionPolicy):
			fail(w, http.StatusBadRequest, "会话 ID 必须有效且不重复，最多 100 个。附件读取须在回复范围内，附件回复须在附件读取范围内")
		case errors.Is(err, apphost.ErrLifecycleConflict):
			fail(w, http.StatusConflict, "正在停止旧的回复任务，请稍后查询连接状态")
		case err != nil:
			fail(w, http.StatusServiceUnavailable, "暂时无法保存回复范围")
		default:
			respond(w, http.StatusOK, map[string]any{"connection": view})
		}
	}))
	mux.HandleFunc("POST /api/apps/{app}/connections/{connection}/lifecycle", s.protected(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var input struct {
			Action string `json:"action"`
		}
		if !decode(w, r, &input) {
			return
		}
		if input.Action != "start" && input.Action != "stop" {
			fail(w, http.StatusBadRequest, "请选择启动或停止应用连接")
			return
		}
		app, ok := registry.Lookup(r.PathValue("app"))
		if !ok {
			fail(w, http.StatusNotFound, "应用未登记")
			return
		}
		controller, ok := app.Provider.(apphost.LifecycleController)
		if !ok {
			fail(w, http.StatusNotImplemented, "应用未接入启停控制")
			return
		}
		status, err := controller.SetConnectionRunning(r.Context(), r.PathValue("connection"), input.Action == "start")
		switch {
		case errors.Is(err, apphost.ErrNotFound):
			fail(w, http.StatusNotFound, "应用连接不存在")
		case errors.Is(err, apphost.ErrLifecycleConflict):
			fail(w, http.StatusConflict, "连接正在收尾或主程序正在维护，请查询连接状态后再试")
		case errors.Is(err, apphost.ErrLifecycleNotReady):
			fail(w, http.StatusConflict, "请先完成模型配置并启用回复会话")
		case err != nil:
			fail(w, http.StatusServiceUnavailable, "暂时无法保存应用连接状态")
		default:
			respond(w, http.StatusOK, status)
		}
	}))
}

func isAppRegistryPath(path string) bool {
	return path == "/api/apps" || strings.HasPrefix(path, "/api/apps/")
}
