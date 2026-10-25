package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Recovery reads the actual incoming message, preserving its recipient and scope.
// A deterministic task and the existing effect ledger prevent double generation.
func (s *Server) recoverMusicMessage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MessageID string `json:"message_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	m := s.manager
	m.op.Lock()
	defer m.op.Unlock()
	msg, err := m.native.Message(r.Context(), in.MessageID)
	if err != nil || msg.ID != in.MessageID || msg.Direction != "incoming" || !permittedRecipient(m.Config().Config, msg.ThreadID) {
		fail(w, 400, "未找到已授权会话中的接收消息")
		return
	}
	at, err := time.Parse(time.RFC3339Nano, msg.CreatedAt)
	if err != nil || time.Since(at) > 24*time.Hour || at.After(time.Now().Add(time.Minute)) || !strings.Contains(msg.Body, "歌") {
		fail(w, 400, "仅支持24小时内的歌曲请求")
		return
	}
	ac := m.agent.config()
	if !ac.Enabled || !ac.TasksEnabled || !m.speechConfig().YuEEnabled {
		fail(w, 409, "请先开启Agent持续任务和本地歌曲生成")
		return
	}
	id := agentID(m.native.Account(), msg.ThreadID, msg.ID, "manual-music-recovery")
	for _, t := range m.agent.list(m.native.Account(), msg.ThreadID) {
		if t.ID == id || t.OriginTrace == msg.ID && t.MusicJob != "" {
			respond(w, 200, t)
			return
		}
	}
	m.agent.mu.Lock()
	exists := false
	for _, op := range m.agent.data.Operations {
		if op.Thread != msg.ThreadID || op.Tool != "music_local_start" {
			continue
		}
		var result map[string]any
		_ = json.Unmarshal([]byte(op.Result), &result)
		if op.ID == agentID(m.native.Account(), msg.ThreadID, msg.ID, op.Tool, string(op.Arguments)) {
			exists = true
			break
		}
	}
	m.agent.mu.Unlock()
	if exists {
		fail(w, 409, "原消息已有生成操作记录，请检查原任务，未重复生成")
		return
	}
	t := agentTask{ID: id, Account: m.native.Account(), Thread: msg.ThreadID, Sender: msg.SenderID, Title: "补办歌曲请求", Checkpoint: msg.Body, State: "pending", Next: time.Now(), OriginTrace: msg.ID, QuoteServerID: msg.ServerID, RecoveryMessageID: msg.ID}
	if err := m.agent.task(t); err != nil {
		fail(w, 500, err.Error())
		return
	}
	respond(w, 201, t)
}
