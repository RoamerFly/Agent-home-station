package admin

import (
	"net/http"
	"strings"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

// Only recover a real incoming private message that the old prefix rule
// skipped. Never invent a sender/body or replay already-processed messages.
func (s *Server) recoverPrivatePrefixMessage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MessageID string `json:"message_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	m := s.manager
	m.op.Lock()
	defer m.op.Unlock()
	msg, e := m.native.Message(r.Context(), in.MessageID)
	if e != nil || msg.ID != in.MessageID || msg.Direction != "incoming" || msg.Type != 1 || strings.HasSuffix(msg.ThreadID, "@chatroom") || msg.ThreadID == "filehelper" || !permittedRecipient(m.Config().Config, msg.ThreadID) {
		fail(w, 400, "未找到已允许私聊中的真实接收文字")
		return
	}
	at, e := time.Parse(time.RFC3339Nano, msg.CreatedAt)
	if e != nil || time.Since(at) > 24*time.Hour || at.After(time.Now().Add(time.Minute)) {
		fail(w, 400, "仅补办24小时内的消息")
		return
	}
	id := agentID(m.native.Account(), msg.ThreadID, msg.ID, "private-prefix-recovery")
	for _, t := range m.agent.list(m.native.Account(), msg.ThreadID) {
		if t.ID == id {
			respond(w, 200, t)
			return
		}
	}
	skipped, processed := false, false
	m.mu.Lock()
	for _, v := range m.journal {
		if v.MessageID == msg.ID && v.ThreadID == msg.ThreadID {
			skipped = skipped || v.Event == "message_skipped" && v.Reason == "private_prefix_not_matched_or_empty"
			processed = processed || v.Event == "message_processing" || v.Event == "message_sent"
		}
	}
	m.mu.Unlock()
	if !skipped || processed {
		fail(w, 409, "只补办曾被旧私信前缀规则跳过且尚未处理的消息")
		return
	}
	cfg := m.agent.config()
	if !cfg.Enabled || !cfg.TasksEnabled {
		fail(w, 409, "Agent任务未启用")
		return
	}
	body := strings.TrimSpace(strings.TrimPrefix(msg.Body, m.Config().Prefix))
	if body == "" || body == strings.TrimSpace(m.Config().Prefix) {
		fail(w, 400, "正文为空")
		return
	}
	access := &permittedTools{manager: m, trace: companion.Input{Account: m.native.Account(), Thread: msg.ThreadID, Sender: msg.SenderID}}
	task := agentTask{ID: id, Account: m.native.Account(), Thread: msg.ThreadID, Sender: msg.SenderID, OwnerRequested: access.privileged(), Title: "补办私聊消息", RequestedText: body, Checkpoint: body, State: "pending", Next: time.Now(), OriginTrace: msg.ID, QuoteServerID: msg.ServerID, RecoveryMessageID: msg.ID}
	if e := m.agent.task(task); e != nil {
		fail(w, 500, "补办任务保存失败")
		return
	}
	m.record(bot.Record{Event: "private_prefix_recovery_queued", Category: "decision", ThreadID: msg.ThreadID, MessageID: msg.ID, Reason: "original_incoming_identity_preserved", Status: "pending", Origin: "admin"})
	respond(w, 201, task)
}
