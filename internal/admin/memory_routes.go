package admin

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"
	"wechat-bot/internal/companion"
)

func (s *Server) memoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/memory/{scope}/entries/{id}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		scope := r.PathValue("scope")
		if _, e := s.manager.memoryScope(scope); e != nil {
			fail(w, 404, "会话不存在")
			return
		}
		m, revisions, e := s.manager.memory.MemoryHistory(r.Context(), []string{scope}, r.PathValue("id"))
		if errors.Is(e, sql.ErrNoRows) {
			fail(w, 404, "记忆已不可用或已遗忘")
			return
		}
		if e != nil {
			fail(w, 500, "修订读取失败")
			return
		}
		respond(w, 200, struct {
			companion.MemoryEntry
			Revisions []companion.MemoryEntry `json:"revisions"`
		}{m, revisions})
	}))
	mux.HandleFunc("GET /api/memory/{scope}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("scope")
		if _, e := s.manager.memoryScope(id); e != nil {
			fail(w, 404, "会话不存在")
			return
		}
		q := companion.MemoryQuery{Scopes: []string{id}, Query: r.URL.Query().Get("query"), Cursor: r.URL.Query().Get("cursor"), Kind: r.URL.Query().Get("kind"), Subject: r.URL.Query().Get("subject"), Since: r.URL.Query().Get("since"), Before: r.URL.Query().Get("before"), Originals: r.URL.Query().Get("originals") == "true"}
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, e := strconv.Atoi(raw)
			if e != nil {
				fail(w, 400, "分页条数无效")
				return
			}
			q.Limit = n
		}
		page, e := s.manager.memory.SearchMemory(r.Context(), q)
		if e != nil {
			fail(w, 409, "查询失败或记忆已更新，请刷新后重试")
			return
		}
		status, e := s.manager.memory.MemoryStatus(r.Context(), id)
		if e != nil {
			fail(w, 500, "记忆状态读取失败")
			return
		}
		respond(w, 200, map[string]any{"page": page, "status": status})
	}))
	mux.HandleFunc("GET /api/memory/{scope}/sources/{id}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		scope := r.PathValue("scope")
		if _, e := s.manager.memoryScope(scope); e != nil {
			fail(w, 404, "会话不存在")
			return
		}
		ev, e := s.manager.memory.ReadEvent(r.Context(), []string{scope}, r.PathValue("id"))
		if e != nil {
			fail(w, 404, "原始来源已不可用或已遗忘")
			return
		}
		respond(w, 200, ev)
	}))
	mux.HandleFunc("POST /api/memory/{scope}/entries/{id}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		scope := r.PathValue("scope")
		if _, e := s.manager.memoryScope(scope); e != nil {
			fail(w, 404, "会话不存在")
			return
		}
		var in struct {
			Action     string                      `json:"action"`
			Version    int                         `json:"expected_revision"`
			Detail     string                      `json:"detail"`
			Assertions []companion.MemoryAssertion `json:"assertions"`
			Sources    []companion.MemorySource    `json:"sources"`
		}
		if !decode(w, r, &in) {
			return
		}
		m, e := s.manager.memory.ReadMemory(r.Context(), []string{scope}, r.PathValue("id"))
		if e != nil {
			fail(w, 404, "记忆不存在或已遗忘")
			return
		}
		if m.Version != in.Version {
			fail(w, 409, "记忆版本已改变，请重新读取")
			return
		}
		switch in.Action {
		case "forget":
			m.Status = "deleted"
		case "pin":
			m.Pinned = true
		case "unpin":
			m.Pinned = false
		case "correct":
			m.Detail = in.Detail
			m.Summary = in.Detail
			m.Sources = in.Sources
			m.Assertions = in.Assertions
		default:
			fail(w, 400, "未知记忆操作")
			return
		}
		m, e = s.manager.memory.SaveMemory(r.Context(), m, in.Version)
		if e != nil {
			fail(w, 409, "保存失败，请核对来源与版本")
			return
		}
		respond(w, 200, m)
	}))
	mux.HandleFunc("GET /api/memory/status", s.protected(func(w http.ResponseWriter, r *http.Request) {
		status, e := s.manager.memory.MemoryStatus(r.Context(), "")
		if e != nil {
			fail(w, 500, "记忆状态不可读")
			return
		}
		cfg, e := s.manager.loadMemoryServices()
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		status["semantic_configured"] = cfg.Embedding.Configured()
		status["settings"] = cfg.Settings
		status["at"] = time.Now().UTC()
		if metrics, e := s.manager.memory.ContextMetrics(r.Context()); e == nil {
			status["context_metrics"] = metrics
		}
		respond(w, 200, status)
	}))
}
