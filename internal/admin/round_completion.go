package admin

import (
	"encoding/json"
	"net/http"
	"strings"
)

type manualRoundCompletionRequest struct {
	MessageID string `json:"message_id"`
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
