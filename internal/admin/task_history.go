package admin

import (
	"net/http"
	"strconv"
)

func (m *Manager) settleBacklogReviews(account, thread string, ids []string, action string) {
	if action != "reply" && action != "silent" && action != "wait" {
		return
	}
	selected := map[string]bool{}
	for _, id := range ids {
		selected[id] = true
	}
	for _, task := range m.agent.list(account, thread) {
		if task.RecoveryMessageID == "" || !selected[task.RecoveryMessageID] || task.Delivery != nil {
			continue
		}
		task.State = "completed"
		task.LastResult = "已纳入本会话离线未读汇总，由模型统一判断；回复投递见该执行回合"
		if err := m.agent.task(task); err != nil {
			m.log("backlog_task_settlement_failed")
			return
		}
	}
	if err := m.agent.retireSettled(); err != nil {
		m.log("task_archive_save_failed")
	}
}
func (s *Server) taskArchiveRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agent/history", s.protected(func(w http.ResponseWriter, r *http.Request) {
		offset, limit := 0, 30
		var err error
		if raw := r.URL.Query().Get("offset"); raw != "" {
			offset, err = strconv.Atoi(raw)
			if err != nil || offset < 0 {
				fail(w, 400, "历史起点无效")
				return
			}
		}
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 0 {
				fail(w, 400, "历史数量无效")
				return
			}
		}
		items := s.manager.agent.taskHistory(s.manager.native.Account(), "")
		start := min(offset, len(items))
		end := len(items)
		if limit > 0 && limit < len(items)-start {
			end = start + limit
		}
		respond(w, 200, map[string]any{"items": items[start:end], "offset": start, "next_offset": end, "has_more": end < len(items), "total": len(items)})
	}))
}
