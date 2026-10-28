package admin

import (
	"encoding/json"
	"net/http"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

// Console requests are authenticated management actions, not forged incoming
// WeChat messages. They reuse the same owner authorization and durable queue as
// Agent tool calls and never create a conversational reply as a side effect.
func (s *Server) crossMediaConsoleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/agent/media-batches", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Owner string `json:"owner_id"`
			crossMediaInput
		}
		if !decode(w, r, &in) {
			return
		}
		m := s.manager
		p := &permittedTools{manager: m, trace: companion.Input{Channel: "wechat", Account: m.native.Account(), Thread: in.Owner, Sender: in.Owner, TraceID: "authenticated-admin-media", OriginTraceID: "authenticated-admin-media-" + agentID(in.Key)}}
		if in.Owner == "" || !p.privileged() || !m.agent.config().CrossConversation {
			fail(w, 403, "需要当前账号已授权的主人及跨会话权限")
			return
		}
		// Both ends must be explicit in a management action; never default the
		// destination to the owner's private chat when the console omits it.
		if in.Source == "" || in.Target == "" {
			fail(w, 400, "请明确来源会话和目标会话")
			return
		}
		args, _ := json.Marshal(in.crossMediaInput)
		result, err := p.sendMedia(r.Context(), args)
		if err != nil {
			fail(w, 400, "附件投递参数或授权无效")
			return
		}
		m.record(bot.Record{Event: "admin_cross_media_request", Category: "tool", ThreadID: in.Target, Sender: in.Owner, MessageID: p.trace.TraceID, Origin: "admin", Reason: "synthetic_authenticated_admin_action"})
		respond(w, 202, map[string]any{"result": json.RawMessage(result), "origin": "synthetic_authenticated_admin_action", "conversational_replies": 0})
	}))
	mux.HandleFunc("POST /api/agent/media-batches/{id}/reconcile", s.protected(func(w http.ResponseWriter, r *http.Request) {
		m := s.manager
		batch, err := m.readCrossBatch(r.PathValue("id"))
		if err != nil {
			fail(w, 404, "未找到当前账号的有效投递批次")
			return
		}
		p := &permittedTools{manager: m, trace: companion.Input{Account: batch.Account, Thread: batch.Origin, Sender: batch.Sender}}
		if !p.crossAllowed(batch.Source, batch.Target) {
			fail(w, 403, "原投递权限已失效")
			return
		}
		result, err := p.crossMediaStatus(r.Context(), batch.ID)
		if err != nil {
			fail(w, 502, "原批次回执暂不可核对，请继续查询同一批次")
			return
		}
		respond(w, 200, map[string]any{"result": json.RawMessage(result), "real_wechat_sends": 0})
	}))
}
