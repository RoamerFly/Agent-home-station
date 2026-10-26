package admin

import (
	"net/http"
	"strings"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

// An authenticated management request is explicitly synthetic. It never
// fabricates a native incoming packet, nickname-based identity or receipt.
func (s *Server) ownerRequestRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/agent/owner-requests", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Recipient string `json:"recipient"`
			Text      string `json:"text"`
			Key       string `json:"request_key"`
		}
		if !decode(w, r, &in) {
			return
		}
		m := s.manager
		c := m.agent.config()
		if !c.Enabled || !c.TasksEnabled || !m.Config().Companion.Enabled || !contains(c.OwnerIDs, in.Recipient) || !permittedRecipient(m.Config().Config, in.Recipient) || strings.HasSuffix(in.Recipient, "@chatroom") {
			fail(w, 403, "仅支持已授权主人的私聊请求，未提升权限")
			return
		}
		p := &permittedTools{manager: m, trace: companion.Input{Account: m.native.Account(), Thread: in.Recipient, Sender: in.Recipient}}
		if !p.privileged() {
			fail(w, 403, "当前主人或会话未获授权")
			return
		}
		if strings.TrimSpace(in.Text) == "" || len(in.Text) > 6000 || in.Key == "" || len(in.Key) > 128 {
			fail(w, 400, "需要正文和稳定的request_key")
			return
		}
		id := agentID(m.native.Account(), in.Recipient, "authenticated-owner-request", in.Key)
		m.op.Lock()
		defer m.op.Unlock()
		for _, old := range m.agent.list(m.native.Account(), in.Recipient) {
			if old.ID == id {
				if old.RequestedText != in.Text {
					fail(w, 409, "同一请求键的正文已改变，未重新执行")
					return
				}
				respond(w, 200, map[string]any{"task": old, "inbound": "synthetic_authenticated_admin_request"})
				return
			}
		}
		t := agentTask{OwnerRequest: true, OwnerRequested: true, ID: id, Account: m.native.Account(), Thread: in.Recipient, Sender: in.Recipient, Title: "管理端模拟微信请求", RequestedText: in.Text, Checkpoint: in.Text, State: "pending", Next: time.Now(), OriginTrace: "admin-owner-request-" + id}
		if e := m.agent.task(t); e != nil {
			fail(w, 500, "请求保存失败")
			return
		}
		m.record(bot.Record{Event: "owner_request_queued", Category: "decision", ThreadID: t.Thread, MessageID: t.OriginTrace, Status: "pending", Reason: "synthetic_authenticated_admin_request", Origin: "admin"})
		respond(w, 202, map[string]any{"task": t, "inbound": "synthetic_authenticated_admin_request"})
	}))
	mux.HandleFunc("GET /api/agent/capabilities", s.protected(func(w http.ResponseWriter, r *http.Request) {
		owner := r.URL.Query().Get("owner")
		m := s.manager
		if !contains(m.agent.config().OwnerIDs, owner) || !permittedRecipient(m.Config().Config, owner) {
			fail(w, 403, "需要已配置主人")
			return
		}
		p, _, e := m.toolAccess()
		if e != nil || p == nil {
			fail(w, 503, "工具目录暂不可用")
			return
		}
		defer p.hub.Close()
		p.trace = companion.Input{Account: m.native.Account(), Thread: owner, Sender: owner, Mode: "direct"}
		catalog, e := p.List(r.Context())
		if e != nil && len(catalog) == 0 {
			fail(w, 503, "工具目录读取失败")
			return
		}
		respond(w, 200, map[string]any{"tools": catalog, "partial": e != nil, "autonomous": p.AutonomousActions(), "host": m.agent.config().Host, "identity": "management service OS user; Windows interop uses the current Windows user"})
	}))
}
