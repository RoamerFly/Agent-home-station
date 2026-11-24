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
	found, err := s.roundExistsInHistory(in.ThreadID, in.MessageID)
	if err != nil {
		fail(w, http.StatusConflict, "执行回合历史记录暂不可读；关闭操作未生效")
		return
	}
	if !found {
		fail(w, http.StatusNotFound, "没有找到这条执行回合")
		return
	}
	worker := m.currentWorker()
	if worker == nil {
		fail(w, http.StatusConflict, "原 Agent 当前未运行，无法确认后续处理已停止；关闭操作未生效")
		return
	}
	if err := worker.ManuallyCompleteRound(r.Context(), in.ThreadID, in.MessageID); err != nil {
		if strings.Contains(err.Error(), "trace_not_found") {
			fail(w, http.StatusConflict, "原回合不在当前微信工作进程队列中；关闭操作未生效")
			return
		}
		fail(w, http.StatusConflict, "关闭操作未生效："+err.Error())
		return
	}
	m.record(bot.Record{Event: "round_manually_closed", Category: "decision", ThreadID: in.ThreadID,
		MessageID: in.MessageID, Status: "stopped", Reason: "owner_closed_round", Origin: "admin"})
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
