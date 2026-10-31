package admin

import (
	"net/http"
	"wechat-bot/internal/companion"
)

// Source repair is an authenticated management operation. It never submits a
// WeChat message or replays tasks; original events remain in the archive.
func (s *Server) sourceRepairRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/memory/{scope}/source-conflicts", s.protected(func(w http.ResponseWriter, r *http.Request) {
		m := s.manager
		m.op.Lock()
		defer m.op.Unlock()
		scope := r.PathValue("scope")
		if _, err := m.memoryScope(scope); err != nil {
			fail(w, 404, "会话不存在")
			return
		}
		m.mu.Lock()
		stopped := m.status.State == "disabled"
		m.mu.Unlock()
		if !stopped {
			fail(w, 409, "请先暂停 Agent，再处理来源冲突")
			return
		}
		var in struct {
			Items []companion.SourceAudit `json:"items"`
		}
		if !decode(w, r, &in) {
			return
		}
		if len(in.Items) == 0 {
			fail(w, 400, "请选择已核对的来源")
			return
		}
		for _, item := range in.Items {
			if item.Scope != scope {
				fail(w, 403, "来源不属于当前会话")
				return
			}
		}
		if err := m.memory.QuarantineSources(r.Context(), in.Items); err != nil {
			fail(w, 409, "来源已更新，未执行隔离，请重新核对")
			return
		}
		respond(w, 200, map[string]any{"state": "quarantined", "count": len(in.Items), "originals_preserved": true, "wechat_sends": 0})
	}))
	mux.HandleFunc("POST /api/memory/{scope}/relationship-correction", s.protected(func(w http.ResponseWriter, r *http.Request) {
		m := s.manager
		m.op.Lock()
		defer m.op.Unlock()
		scope, err := m.memoryScope(r.PathValue("scope"))
		if err != nil {
			fail(w, 404, "会话不存在")
			return
		}
		var in struct {
			Requester        string                      `json:"requester"`
			OriginalID       string                      `json:"original_event_id"`
			ExpectedRevision int                         `json:"expected_revision"`
			Detail           string                      `json:"detail"`
			Assertions       []companion.MemoryAssertion `json:"assertions"`
		}
		if !decode(w, r, &in) {
			return
		}
		p := &permittedTools{manager: m, trace: companion.Input{Account: m.native.Account(), Thread: scope.Thread, Sender: in.Requester}}
		if !p.privileged() {
			fail(w, 403, "需要选择本账号已验证的主人")
			return
		}
		entry, err := m.memory.SaveSourcedAdminCorrection(r.Context(), scope.ID, in.Requester, in.OriginalID, in.ExpectedRevision, in.Detail, in.Assertions)
		if err != nil {
			fail(w, 409, "更正未保存，请核对原始来源、关系与版本")
			return
		}
		respond(w, 200, entry)
	}))
}
