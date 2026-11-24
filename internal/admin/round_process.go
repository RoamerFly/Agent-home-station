package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"wechat-bot/internal/bot"
)

// manualRoundProcess asks the running WeChat actor to continue one original
// message. The actor owns its persisted send intent and receipt; the console
// never replays a saved draft or edits its queue directly.
func (s *Server) manualRoundProcess(w http.ResponseWriter, r *http.Request) {
	var in manualRoundCompletionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil ||
		strings.TrimSpace(in.MessageID) == "" || strings.TrimSpace(in.ThreadID) == "" || len(in.MessageID) > 512 || len(in.ThreadID) > 256 {
		fail(w, http.StatusBadRequest, "执行回合编号无效")
		return
	}
	worker := s.manager.currentWorker()
	if worker == nil {
		fail(w, http.StatusConflict, "原 Agent 当前未运行，尚未触发处理；请先恢复该微信工作进程")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	closed, err := s.roundWasClosed(in.ThreadID, in.MessageID)
	if err != nil {
		fail(w, http.StatusConflict, "旧回合关闭记录暂不可读；没有触发处理")
		return
	}
	if closed {
		if err := worker.ManuallyCompleteRound(ctx, in.ThreadID, in.MessageID); err != nil && !strings.Contains(err.Error(), "trace_not_found") {
			fail(w, http.StatusConflict, "旧回合已关闭，持久关闭状态暂未同步："+err.Error())
			return
		}
		respond(w, http.StatusOK, map[string]any{"item": map[string]string{"message_id": in.MessageID, "status": "unavailable", "reason": "该执行回合已关闭，不会重新处理"}})
		return
	}
	item, err := worker.TriggerRoundProcessing(ctx, in.ThreadID, in.MessageID)
	if err != nil {
		fail(w, http.StatusConflict, "手动触发未生效："+err.Error())
		return
	}
	respond(w, http.StatusOK, map[string]any{"item": item})
}

// Older releases recorded a close only in activity. Check that history before
// allowing a manual restart, then migrate the decision into the actor's
// durable queue. No archived message text is returned to the browser.
func (s *Server) roundWasClosed(thread, message string) (bool, error) {
	return s.manager.visitRoundHistory(func(entry LogEntry) bool {
		return entry.Event == "round_manually_closed" && entry.ThreadID == thread && entry.MessageID == message
	})
}

func (s *Server) roundExistsInHistory(thread, message string) (bool, error) {
	return s.manager.visitRoundHistory(func(entry LogEntry) bool {
		return entry.ThreadID == thread && entry.MessageID == message
	})
}

func (m *Manager) closedRoundHistory() ([]bot.RoundIdentity, error) {
	seen := map[bot.RoundIdentity]bool{}
	_, err := m.visitRoundHistory(func(entry LogEntry) bool {
		if entry.Event == "round_manually_closed" && entry.ThreadID != "" && entry.MessageID != "" {
			seen[bot.RoundIdentity{Thread: entry.ThreadID, Trace: entry.MessageID}] = true
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	result := make([]bot.RoundIdentity, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	return result, nil
}

func (m *Manager) visitRoundHistory(visit func(LogEntry) bool) (bool, error) {
	m.mu.Lock()
	current := append(append([]LogEntry{}, m.logs...), m.journal...)
	m.mu.Unlock()
	for _, entry := range current {
		if visit(entry) {
			return true, nil
		}
	}
	archive := m.historyArchive()
	after := ""
	for {
		infos, next, err := archive.List("activity", activityScope(), after, 50)
		if err != nil {
			return false, err
		}
		for _, info := range infos {
			batch, _, err := archive.Read("activity", activityScope(), info.ID)
			if err != nil {
				return false, err
			}
			var entries []LogEntry
			if err := json.Unmarshal(batch.Data, &entries); err != nil {
				return false, err
			}
			for _, entry := range entries {
				if visit(entry) {
					return true, nil
				}
			}
		}
		if next == "" {
			return false, nil
		}
		after = next
	}
}
