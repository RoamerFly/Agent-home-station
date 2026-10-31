package admin

import (
	"net/http"
	"strconv"
	"wechat-bot/internal/companion"
)

func (s *Server) replyAuditRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agent/runtime-capabilities", s.protected(func(w http.ResponseWriter, r *http.Request) {
		result, err := s.manager.InspectRegisteredCapabilities(r.Context())
		if err != nil {
			fail(w, 500, "能力登记暂不可读")
			return
		}
		respond(w, 200, result)
	}))
	mux.HandleFunc("GET /api/reply-audits", s.protected(func(w http.ResponseWriter, r *http.Request) {
		thread := r.URL.Query().Get("thread_id")
		if thread == "" {
			fail(w, 400, "请选择会话")
			return
		}
		scope := companion.ScopeID("wechat", s.manager.native.Account(), thread)
		s.replyAuditList(w, r, scope)
	}))
	mux.HandleFunc("GET /api/memory/{scope}/reply-audits", s.protected(func(w http.ResponseWriter, r *http.Request) {
		s.replyAuditList(w, r, r.PathValue("scope"))
	}))
	mux.HandleFunc("GET /api/memory/{scope}/reply-audits/{id}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		scope := r.PathValue("scope")
		if _, err := s.manager.memoryScope(scope); err != nil {
			fail(w, 404, "会话不存在")
			return
		}
		services, err := s.manager.loadMemoryServices()
		if err != nil {
			fail(w, 500, "记忆设置暂不可读")
			return
		}
		audit, err := s.manager.memory.ReadReplyAudit(r.Context(), scope, r.PathValue("id"), services.Settings.AuditConfig())
		if err != nil {
			fail(w, 404, "回复依据已过期或不可用")
			return
		}
		respond(w, 200, audit)
	}))
}

func (s *Server) replyAuditList(w http.ResponseWriter, r *http.Request, scope string) {
	if _, err := s.manager.memoryScope(scope); err != nil {
		fail(w, 404, "会话不存在")
		return
	}
	limit, offset := 50, 0
	for key, dst := range map[string]*int{"limit": &limit, "offset": &offset} {
		if raw := r.URL.Query().Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 || key == "limit" && value == 0 {
				fail(w, 400, "分页参数无效")
				return
			}
			*dst = value
		}
	}
	services, err := s.manager.loadMemoryServices()
	if err != nil {
		fail(w, 500, "记忆设置暂不可读")
		return
	}
	items, more, err := s.manager.memory.FindReplyAudits(r.Context(), scope, r.URL.Query().Get("message_id"), offset, limit, services.Settings.AuditConfig())
	if err != nil {
		fail(w, 500, "回复依据暂不可读")
		return
	}
	respond(w, 200, map[string]any{"scope": scope, "items": items, "has_more": more, "next_offset": offset + len(items), "effective_limit": limit})
}
