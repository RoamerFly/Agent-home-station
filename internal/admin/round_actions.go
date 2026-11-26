package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type roundActionRequest struct {
	ThreadID   string   `json:"thread_id"`
	MessageID  string   `json:"message_id"`
	TaskID     string   `json:"task_id,omitempty"`
	Mode       string   `json:"mode,omitempty"`
	MessageIDs []string `json:"message_ids,omitempty"`
}

// A fresh retry does not expose old task checkpoints through agent_tasks in
// this one model turn. Shared persisted tasks and other conversations remain
// untouched.
func freshRoundHidesTask(in companion.Input, task agentTask) bool {
	if !in.FreshRoundRetry && !in.FreshRoundLineage {
		return false
	}
	if task.ID == in.TaskID || task.ParentID == in.TaskID {
		return false
	}
	if task.RoundRetryFresh && task.ID != in.FreshRoundPriorTaskID && task.OriginTrace == in.OriginTraceID {
		return false
	}
	return in.FreshRoundPriorTaskID != "" && (task.ID == in.FreshRoundPriorTaskID || task.ParentID == in.FreshRoundPriorTaskID) ||
		in.OriginTraceID != "" && task.OriginTrace == in.OriginTraceID
}

func decodeRoundAction(w http.ResponseWriter, r *http.Request) (roundActionRequest, bool) {
	var in roundActionRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in) != nil {
		fail(w, http.StatusBadRequest, "执行回合参数无效")
		return in, false
	}
	in.ThreadID, in.MessageID, in.TaskID = strings.TrimSpace(in.ThreadID), strings.TrimSpace(in.MessageID), strings.TrimSpace(in.TaskID)
	if in.ThreadID == "" || in.MessageID == "" || len(in.ThreadID) > 256 || len(in.MessageID) > 512 || len(in.TaskID) > 128 || strings.ContainsAny(in.ThreadID+in.MessageID+in.TaskID, "\x00\r\n") {
		fail(w, http.StatusBadRequest, "执行回合编号无效")
		return in, false
	}
	traceTask := continuationTaskID(in.MessageID)
	if traceTask != in.TaskID {
		fail(w, http.StatusBadRequest, "任务编号与执行回合不匹配")
		return in, false
	}
	if len(in.MessageIDs) > 30 {
		fail(w, http.StatusBadRequest, "合并消息数量无效")
		return in, false
	}
	return in, true
}

func (s *Server) cancelRound(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeRoundAction(w, r)
	if !ok {
		return
	}
	if found, err := s.roundExistsInHistory(in.ThreadID, in.MessageID); err != nil || !found {
		if err != nil {
			fail(w, http.StatusConflict, "执行回合记录暂不可读；尚未取消")
		} else {
			fail(w, http.StatusNotFound, "没有找到这条执行回合")
		}
		return
	}
	if err := s.stopRound(r.Context(), in, ""); err != nil {
		fail(w, http.StatusConflict, "取消回合未完成："+err.Error())
		return
	}
	s.recordClosedRoundAttempts(in, "owner_cancelled_round")
	respond(w, http.StatusOK, map[string]any{"cancelled": true})
}

// stopRound closes all durable work reachable from this round. The WeChat
// ledger and accepted receipts are deliberately left in place.
func (s *Server) stopRound(ctx context.Context, in roundActionRequest, exceptTask string) error {
	stoppedTasks, err := s.cancelRoundTasks(in, exceptTask)
	if err != nil {
		return err
	}
	if worker := s.manager.currentWorker(); worker != nil {
		bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		for _, task := range stoppedTasks {
			if err := worker.ManuallyCompleteTask(bounded, task.Thread, task.ID); err != nil {
				return err
			}
		}
		if err := worker.ManuallyCompleteRound(bounded, in.ThreadID, in.MessageID); err != nil && !strings.Contains(err.Error(), "trace_not_found") {
			return err
		}
	}
	return nil
}

func (s *Server) cancelRoundTasks(in roundActionRequest, exceptTask string) ([]agentTask, error) {
	account := s.manager.native.Account()
	all := s.manager.agent.active(account, "")
	selected := map[string]bool{}
	for _, task := range all {
		if in.TaskID != "" {
			if task.ID == in.TaskID {
				selected[task.ID] = true
			}
		} else if task.Thread == in.ThreadID && task.OriginTrace == in.MessageID {
			selected[task.ID] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, task := range all {
			if task.ParentID != "" && selected[task.ParentID] && !selected[task.ID] {
				selected[task.ID], changed = true, true
			}
		}
	}
	stopped := []agentTask{}
	for _, task := range all {
		if !selected[task.ID] || task.ID == exceptTask {
			continue
		}
		stopped = append(stopped, task)
		if task.State == "cancelled" || task.State == "completed" || task.State == "stopped" {
			continue
		}
		task.State, task.Next, task.LastResult = "cancelled", time.Time{}, "执行回合已取消，后续不再调度"
		if saved, err := s.manager.agent.updateSnapshot(task); err != nil {
			return nil, err
		} else if !saved {
			return nil, errors.New("关联任务状态已变化，请刷新后重试")
		}
	}
	return stopped, s.manager.agent.retireSettled()
}

func (s *Server) retryRound(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeRoundAction(w, r)
	if !ok {
		return
	}
	if in.Mode != "with_memory" && in.Mode != "fresh" {
		fail(w, http.StatusBadRequest, "请选择带记忆或无记忆再试")
		return
	}
	if found, err := s.roundExistsInHistory(in.ThreadID, in.MessageID); err != nil || !found {
		if err != nil {
			fail(w, http.StatusConflict, "执行回合记录暂不可读；尚未重试")
		} else {
			fail(w, http.StatusNotFound, "没有找到这条执行回合")
		}
		return
	}
	if closed, err := s.roundWasClosed(in.ThreadID, in.MessageID); err != nil || closed {
		if err != nil {
			fail(w, http.StatusConflict, "回合关闭记录暂不可读；尚未重试")
		} else {
			fail(w, http.StatusConflict, "该执行回合已取消")
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	task, err := s.retryTaskFromRound(ctx, in)
	if err != nil {
		fail(w, http.StatusConflict, "无法重试这条执行回合："+err.Error())
		return
	}
	// Keep the new attempt dormant until the old queues and task are stopped.
	// Otherwise a busy worker could claim it between the two durable writes.
	task.Next = time.Now().Add(10 * time.Minute)
	if err := s.manager.agent.task(task); err != nil {
		fail(w, http.StatusConflict, "重试任务未保存："+err.Error())
		return
	}
	if err := s.stopRound(ctx, in, task.ID); err != nil {
		_, _ = s.cancelRoundTasks(roundActionRequest{ThreadID: task.Thread, MessageID: task.OriginTrace, TaskID: task.ID}, "")
		fail(w, http.StatusConflict, "旧回合未能停止，新尝试已取消："+err.Error())
		return
	}
	current, exists := s.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !exists {
		fail(w, http.StatusConflict, "新尝试未能读取，请检查持久任务记录")
		return
	}
	current.Next = time.Now()
	if saved, err := s.manager.agent.updateSnapshot(current); err != nil || !saved {
		if err != nil {
			fail(w, http.StatusConflict, "新尝试尚未开始："+err.Error())
		} else {
			fail(w, http.StatusConflict, "新尝试状态变化，请刷新")
		}
		return
	}
	s.recordClosedRoundAttempts(in, "superseded_by_explicit_retry")
	newTrace := "task-" + task.ID + "-" + time.Now().UTC().Format("20060102T150405.000000000")
	s.manager.record(bot.Record{Event: "round_retry_queued", Category: "decision", ThreadID: task.Thread,
		MessageID: newTrace, RelatedMessageID: in.MessageID, Status: "queued", Reason: in.Mode, Origin: "admin"})
	respond(w, http.StatusOK, map[string]any{"queued": true, "task_id": task.ID, "attempt_id": newTrace})
}

func (s *Server) recordClosedRoundAttempts(in roundActionRequest, reason string) {
	traces := map[string]bool{in.MessageID: true}
	if in.TaskID != "" {
		_, _ = s.manager.visitRoundHistory(func(entry LogEntry) bool {
			if entry.ThreadID == in.ThreadID && continuationTaskID(entry.MessageID) == in.TaskID {
				traces[entry.MessageID] = true
			}
			return false
		})
	}
	for trace := range traces {
		s.manager.record(bot.Record{Event: "round_manually_closed", Category: "decision", ThreadID: in.ThreadID,
			MessageID: trace, Status: "stopped", Reason: reason, Origin: "admin"})
	}
}

func (s *Server) retryTaskFromRound(ctx context.Context, in roundActionRequest) (agentTask, error) {
	account := s.manager.native.Account()
	var previous agentTask
	var originalID string
	if in.TaskID != "" {
		var ok bool
		previous, ok = s.manager.agent.lookupTask(in.TaskID, account, in.ThreadID)
		if !ok {
			return agentTask{}, errors.New("原持久任务不存在")
		}
		originalID = previous.OriginTrace
	} else {
		originalID = in.MessageID
	}
	if originalID == "" {
		return agentTask{}, errors.New("原回合没有可读取的微信来源消息")
	}
	message, err := s.manager.native.Message(ctx, originalID)
	if err != nil || message.ID != originalID || message.AccountID != account || message.Direction != "incoming" {
		return agentTask{}, errors.New("原始微信消息当前不可读取")
	}
	if in.TaskID == "" && message.ThreadID != in.ThreadID || in.TaskID != "" &&
		(message.ThreadID != taskSourceThread(previous) || previous.Sender != "" && previous.Sender != message.SenderID) {
		return agentTask{}, errors.New("原始微信消息与执行回合来源不一致")
	}
	if err := s.resolveRoundRetryQuote(ctx, &message); err != nil {
		return agentTask{}, err
	}
	text := retryNativeText(message)
	if in.TaskID != "" && in.Mode == "with_memory" && strings.TrimSpace(previous.RequestedText) != "" {
		text = strings.TrimSpace(previous.RequestedText)
	}
	if text == "" {
		text = map[int]string{3: "请处理我发的图片。", 34: "请处理我发的语音。", 43: "请处理我发的视频。", 47: "请处理我发的表情包。", 49: "请读取我发的分享或文件。"}[message.Type]
	}
	if text == "" {
		return agentTask{}, errors.New("原消息缺少可还原的请求内容")
	}
	projected := bot.NativeMessageInput(message)
	var source *companion.SourceRef
	if previous.Constraints != nil && previous.Constraints.Source != nil {
		copy := *previous.Constraints.Source
		source = &copy
	}
	if source == nil || source.EventID == "" {
		lookup := companion.Input{Channel: "wechat", Account: account, Thread: message.ThreadID, Sender: message.SenderID, TraceID: message.ID}
		if found, ok := s.manager.memory.SourceForInput(lookup); ok {
			source = found
		}
	}
	if source == nil {
		source = projected.Source
	}
	if source != nil && (source.MessageID != originalID || source.Thread != "" && source.Thread != message.ThreadID || source.Sender != "" && source.Sender != message.SenderID) {
		return agentTask{}, errors.New("原消息来源记录与微信原件不一致")
	}
	for i := range projected.IncomingMedia {
		if source != nil && projected.IncomingMedia[i].Source.MessageID == source.MessageID {
			projected.IncomingMedia[i].Source = *source
		}
	}
	unread, batchMedia, err := s.roundRetryUnread(ctx, in, message)
	if err != nil {
		return agentTask{}, err
	}
	now := time.Now().UTC()
	id := agentID("manual-round-retry", account, in.ThreadID, in.MessageID, now.Format(time.RFC3339Nano))
	title := "重新处理原消息"
	if in.Mode == "fresh" {
		title = "处理消息"
	}
	task := agentTask{ID: id, Account: account, Thread: in.ThreadID, Sender: message.SenderID,
		Title: title, RequestedText: text, Goal: text, State: "pending", Next: now,
		OriginTrace: originalID, QuoteServerID: message.ServerID, RoundRetry: true,
		RoundRetryFresh: in.Mode == "fresh", RoundRetryPriorTrace: in.MessageID,
		RoundRetryPriorTaskID: in.TaskID,
		SourceThread:          message.ThreadID, RequestThread: message.ThreadID}
	task.RoundRetryMedia = append([]companion.IncomingMediaRef(nil), projected.IncomingMedia...)
	for _, media := range batchMedia {
		if media.Source.MessageID == message.ID {
			continue
		}
		task.RoundRetryMedia = append(task.RoundRetryMedia, media)
	}
	task.RoundRetryQuote = projected.QuotedSource
	task.RoundRetryUnread = unread
	if source != nil {
		task.Constraints = &TaskConstraints{Purpose: "round_retry", Account: account, Thread: message.ThreadID,
			Requester: message.SenderID, Source: source}
	}
	return task, nil
}

func retryNativeText(message linuxnative.Message) string {
	if body := strings.TrimSpace(message.Body); body != "" {
		return body
	}
	if label, detail := message.CardContext(); label != "" {
		return strings.TrimSpace(label + " " + detail)
	}
	if name := message.FileAttachmentName(); name != "" {
		return "收到文件：" + name
	}
	return map[int]string{3: "收到一张图片", 34: "收到一条语音", 43: "收到一段视频", 47: "收到一条表情包", 49: "收到一条分享"}[message.Type]
}

// The console supplies only candidate IDs. Every member of a reassembled
// collection window is read from native WeChat and verified against the same
// account, conversation and 30-second window before it reaches the new turn.
func (s *Server) roundRetryUnread(ctx context.Context, in roundActionRequest, anchor linuxnative.Message) ([]companion.ConversationEvent, []companion.IncomingMediaRef, error) {
	if len(in.MessageIDs) < 2 {
		return nil, nil, nil
	}
	anchorAt, err := time.Parse(time.RFC3339Nano, anchor.CreatedAt)
	if err != nil {
		return nil, nil, errors.New("原消息时间无效，无法重建合并窗口")
	}
	seen := map[string]bool{}
	result := make([]companion.ConversationEvent, 0, len(in.MessageIDs))
	media := []companion.IncomingMediaRef{}
	for _, id := range in.MessageIDs {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 512 || seen[id] {
			continue
		}
		seen[id] = true
		msg, readErr := s.manager.native.Message(ctx, id)
		at, parseErr := time.Parse(time.RFC3339Nano, msg.CreatedAt)
		if readErr != nil || parseErr != nil || msg.ID != id || msg.Direction != "incoming" || msg.AccountID != anchor.AccountID ||
			msg.ThreadID != anchor.ThreadID || at.Sub(anchorAt) > 30*time.Second || anchorAt.Sub(at) > 30*time.Second {
			return nil, nil, errors.New("合并消息中有无法核对的原始微信消息")
		}
		if err := s.resolveRoundRetryQuote(ctx, &msg); err != nil {
			return nil, nil, err
		}
		projected := bot.NativeMessageInput(msg)
		if ref, ok := s.manager.memory.SourceForInput(projected); ok {
			projected.Source = ref
		}
		for _, item := range projected.IncomingMedia {
			if projected.Source != nil && item.Source.MessageID == projected.Source.MessageID {
				item.Source = *projected.Source
			}
			media = append(media, item)
		}
		result = append(result, companion.ConversationEvent{MessageID: id, Sender: msg.SenderID,
			Text: retryNativeText(msg), At: msg.CreatedAt, Source: projected.Source, ReplyTo: projected.QuotedSource,
			NewRequest: true})
	}
	if !seen[anchor.ID] {
		return nil, nil, errors.New("合并消息缺少原请求")
	}
	return result, media, nil
}

func (s *Server) resolveRoundRetryQuote(ctx context.Context, message *linuxnative.Message) error {
	if message == nil || !message.IsQuotedText() || message.Quote == nil {
		return nil
	}
	reader, ok := s.manager.native.(interface {
		QuotedMessage(context.Context, string) (linuxnative.QuoteResolution, error)
	})
	if !ok {
		return errors.New("原引用消息当前无法读取")
	}
	quote, err := reader.QuotedMessage(ctx, message.ID)
	if err != nil || quote.State != "resolved" || quote.Message == nil || quote.MessageID != message.ID ||
		quote.AccountID != message.AccountID || quote.ThreadID != message.ThreadID || quote.Message.ID == message.ID ||
		quote.Message.AccountID != message.AccountID || quote.Message.ThreadID != message.ThreadID ||
		quote.Message.ServerID != message.Quote.ServerID || quote.Message.Type != message.Quote.Type {
		return errors.New("原引用消息当前无法核对，尚未重试")
	}
	message.ResolvedQuote = quote.Message
	message.ReplyToSelf = quote.Message.Direction == "outgoing"
	return nil
}
