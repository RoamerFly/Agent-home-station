package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"wechat-bot/internal/bot"
)

type manualRoundCompletionRequest struct {
	MessageID string `json:"message_id"`
	ThreadID  string `json:"thread_id"`
}

func (s *Server) manualRoundClose(w http.ResponseWriter, r *http.Request) {
	var in manualRoundCompletionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil ||
		strings.TrimSpace(in.MessageID) == "" || strings.TrimSpace(in.ThreadID) == "" || len(in.MessageID) > 512 || len(in.ThreadID) > 256 {
		fail(w, http.StatusBadRequest, "执行回合编号无效")
		return
	}
	m := s.manager
	m.mu.Lock()
	found := false
	for _, entries := range [][]LogEntry{m.logs, m.journal} {
		for _, entry := range entries {
			if entry.ThreadID == in.ThreadID && entry.MessageID == in.MessageID {
				found = true
				break
			}
		}
	}
	m.mu.Unlock()
	if !found {
		fail(w, http.StatusNotFound, "没有找到这条执行回合")
		return
	}
	m.record(bot.Record{Event: "round_manually_closed", Category: "decision", ThreadID: in.ThreadID,
		MessageID: in.MessageID, Status: "completed", Reason: "owner_closed_round", Origin: "admin"})
	respond(w, http.StatusOK, map[string]any{"closed": true})
}

func (s *Server) manualRoundCompletion(w http.ResponseWriter, r *http.Request) {
	var in manualRoundCompletionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil || strings.TrimSpace(in.MessageID) == "" {
		fail(w, http.StatusBadRequest, "请输入要完成的原消息 ID")
		return
	}
	s.manager.workerMu.RLock()
	worker := s.manager.worker
	s.manager.workerMu.RUnlock()
	if worker == nil {
		fail(w, http.StatusConflict, "Agent 当前未运行")
		return
	}
	if err := worker.ManuallyComplete(r.Context(), in.MessageID); err != nil {
		if strings.Contains(err.Error(), "trace_not_found") {
			fail(w, http.StatusNotFound, "没有找到仍在处理的执行回合")
			return
		}
		fail(w, http.StatusConflict, err.Error())
		return
	}
	respond(w, http.StatusOK, map[string]any{"completed": true, "message_id": in.MessageID})
}
