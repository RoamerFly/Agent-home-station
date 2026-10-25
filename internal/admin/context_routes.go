package admin

import (
	"context"
	"net/http"
	"time"
	"wechat-bot/internal/companion"
)

func (s *Server) contextRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/companion/memory/{id}/compress", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var in struct{}
		if !decode(w, r, &in) {
			return
		}
		scope, err := s.manager.memoryScope(r.PathValue("id"))
		if err != nil {
			fail(w, 404, err.Error())
			return
		}
		cfg := s.manager.runtimeConfig()
		provider, err := s.manager.makeModel(cfg)
		if err != nil {
			fail(w, 400, "模型尚未配置")
			return
		}
		engine, err := companion.New(cfg.Companion, s.manager.memory, provider, cfg.SystemPrompt)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		changed, err := engine.CompressContext(ctx, companion.Input{Channel: scope.Channel, Account: scope.Account, Thread: scope.Thread}, true)
		if err != nil {
			fail(w, 502, "压缩未保存；原始记忆保留，请稍后重试")
			return
		}
		respond(w, 200, map[string]any{"compressed": changed, "scope_id": scope.ID})
	}))
}
