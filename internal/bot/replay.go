package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"wechat-bot/internal/linuxnative"
)

// ReplayItem is the result of an explicit administrator request to reprocess
// an existing native incoming message. A replay never creates a native event.
type ReplayItem struct {
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

type ReplayReport struct {
	Items []ReplayItem `json:"items"`
}

type replayRequest struct {
	ids           []string
	triggerThread string
	done          chan replayResponse
}

type replayResponse struct {
	report ReplayReport
	err    error
}

// ReplayMessages queues a bounded, administrator-initiated review of exact
// native messages. It is intentionally conservative: terminal rounds with a
// prior tool, plan, media delivery or text delivery are not replayed.
func (w *Worker) ReplayMessages(ctx context.Context, ids []string) (ReplayReport, error) {
	if w == nil || w.replayRequests == nil {
		return ReplayReport{}, errors.New("bot_replay_unavailable")
	}
	if len(ids) == 0 || len(ids) > 8 {
		return ReplayReport{}, errors.New("bot_replay_message_count_invalid")
	}
	copyIDs := append([]string(nil), ids...)
	done := make(chan replayResponse, 1)
	select {
	case w.replayRequests <- replayRequest{ids: copyIDs, done: done}:
	case <-ctx.Done():
		return ReplayReport{}, ctx.Err()
	}
	select {
	case response := <-done:
		return response.report, response.err
	case <-ctx.Done():
		return ReplayReport{}, ctx.Err()
	}
}

// TriggerRoundProcessing is an explicit owner action on one original incoming
// message. It reuses an untouched message, or checks one existing native
// receipt. It never submits a second copy of an uncertain delivery.
func (w *Worker) TriggerRoundProcessing(ctx context.Context, threadID, messageID string) (ReplayItem, error) {
	if w == nil || w.replayRequests == nil {
		return ReplayItem{}, errors.New("bot_replay_unavailable")
	}
	threadID, messageID = strings.TrimSpace(threadID), strings.TrimSpace(messageID)
	if threadID == "" || messageID == "" {
		return ReplayItem{}, errors.New("bot_replay_message_id_invalid")
	}
	done := make(chan replayResponse, 1)
	select {
	case w.replayRequests <- replayRequest{ids: []string{messageID}, triggerThread: threadID, done: done}:
	case <-ctx.Done():
		return ReplayItem{}, ctx.Err()
	}
	select {
	case response := <-done:
		if response.err != nil {
			return ReplayItem{}, response.err
		}
		if len(response.report.Items) != 1 {
			return ReplayItem{}, errors.New("bot_replay_result_invalid")
		}
		return response.report.Items[0], nil
	case <-ctx.Done():
		return ReplayItem{}, ctx.Err()
	}
}

func (w *Worker) drainReplayRequests(ctx context.Context) error {
	for {
		select {
		case request := <-w.replayRequests:
			var report ReplayReport
			var err error
			if request.triggerThread != "" {
				var item ReplayItem
				item, err = w.triggerRoundProcessing(ctx, request.triggerThread, request.ids[0])
				report.Items = []ReplayItem{item}
			} else {
				report, err = w.replayMessages(ctx, request.ids)
			}
			request.done <- replayResponse{report: report, err: err}
		case <-ctx.Done():
			return nil
		default:
			return nil
		}
	}
}

func (w *Worker) triggerRoundProcessing(ctx context.Context, threadID, messageID string) (ReplayItem, error) {
	item := ReplayItem{MessageID: messageID}
	if w.runtime == nil {
		item.Status, item.Reason = "unavailable", "Agent 队列尚未就绪"
		return item, nil
	}
	if w.roundClosed(threadID, messageID) {
		item.Status, item.Reason = "unavailable", "执行回合已关闭"
		return item, nil
	}
	message, err := w.source.Message(ctx, messageID)
	if err != nil || message.ID != messageID || message.ThreadID != threadID || message.Direction != "incoming" {
		item.Status, item.Reason = "unavailable", "原消息当前无法读取"
		return item, nil
	}
	if !w.allowed[threadID] {
		item.Status, item.Reason = "unavailable", "当前会话未启用 Agent 处理"
		return item, nil
	}
	var inbox *inboxItem
	for i := range w.runtime.Inbox {
		if w.runtime.Inbox[i].Event.Data.MessageID == messageID && w.runtime.Inbox[i].Event.Data.ThreadID == threadID {
			inbox = &w.runtime.Inbox[i]
			break
		}
	}
	if inbox == nil {
		item.Status, item.Reason = "unavailable", "原消息不在当前 Agent 队列"
		return item, nil
	}
	acceptedPart := false
	for _, d := range w.runtime.Deliveries {
		if d == nil || d.Trace != messageID || d.Thread != threadID {
			continue
		}
		if d.State == "accepted" {
			acceptedPart = true
			continue
		}
		if d.ID == "" && d.SubmittedText == "" && d.ServerID == "" && len(d.PreviousKeys) == 0 {
			continue // An unsubmitted draft may be replaced by a fresh model turn.
		}
		native, ok := w.sender.(reliableNative)
		if !ok || d.Key == "" {
			item.Status, item.Reason = "receipt_pending", "保留原发送记录，当前无法读取微信回执"
			return item, nil
		}
		readCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		status, lookupErr := lookupTextReceipt(readCtx, native, d)
		cancel()
		if lookupErr == nil && status.State == "accepted" {
			if status.ID != "" {
				d.ID = status.ID
			}
			d.ServerID = status.ServerID
			d.AcceptedAt = time.Now().UTC().Format(time.RFC3339Nano)
			w.acceptReconciled(d)
			item.Status, item.Reason = "already_done", "原发送已由微信回执确认"
			for _, plan := range w.runtime.Plans {
				if plan != nil && plan.Input.Thread == threadID && plan.Input.TraceID == messageID && plan.Index < len(plan.Messages) {
					item.Status, item.Reason = "unavailable", "已确认部分回复；剩余内容需按原计划处理"
					break
				}
			}
			w.activity(Record{Event: "round_manual_receipt_checked", Category: "decision", ThreadID: threadID, MessageID: messageID, Status: "accepted", Origin: "admin"})
			return item, w.persistErr
		}
		item.Status, item.Reason = "receipt_pending", "原发送结果仍未确认；没有重新发送"
		w.activity(Record{Event: "round_manual_receipt_checked", Category: "decision", ThreadID: threadID, MessageID: messageID, Status: "waiting", Origin: "admin"})
		return item, nil
	}
	if acceptedPart {
		item.Status, item.Reason = "already_done", "原回复已有微信接收记录"
		for _, plan := range w.runtime.Plans {
			if plan != nil && plan.Input.Thread == threadID && plan.Input.TraceID == messageID && plan.Index < len(plan.Messages) {
				item.Status, item.Reason = "unavailable", "已发送部分回复，不能整轮重复处理"
				break
			}
		}
		return item, nil
	}
	for _, media := range w.runtime.Media {
		if media != nil && media.Trace == messageID {
			item.Status, item.Reason = "unavailable", "原回合已有媒体投递记录，不能整轮重做"
			return item, nil
		}
	}
	for _, media := range w.runtime.MediaReplies {
		if media != nil && media.Trace == messageID {
			item.Status, item.Reason = "unavailable", "原回合已有媒体回复记录，不能整轮重做"
			return item, nil
		}
	}
	if inbox.ToolStarted {
		item.Status, item.Reason = "unavailable", "原回合已有工具操作，不能重复执行"
		return item, nil
	}
	if w.generation != nil {
		if job := w.generation.jobs[threadID]; job != nil && job.input.TraceID == messageID {
			item.Status, item.Reason = "queued", "原回合正在处理"
			return item, nil
		}
	}
	for _, plan := range w.runtime.Plans {
		if plan == nil || plan.Input.Thread != threadID || plan.Input.TraceID != messageID {
			continue
		}
		if plan.State == "accepted" && plan.Index == len(plan.Messages) {
			item.Status, item.Reason = "already_done", "原回合已经完成"
			return item, nil
		}
		if plan.Index > 0 || len(plan.Sent) > 0 {
			item.Status, item.Reason = "unavailable", "原回合已有部分回复，不会整轮重复发送"
			return item, nil
		}
		if len(plan.Updates) > 0 {
			item.Status, item.Reason = "unavailable", "原回合正处理明确纠错"
			return item, nil
		}
	}
	at, parseErr := time.Parse(time.RFC3339Nano, message.CreatedAt)
	backlogHours := 24
	if w.config.Options != nil && w.config.Options.BacklogHours > 0 {
		backlogHours = w.config.Options.BacklogHours
	}
	if parseErr != nil || at.After(time.Now().Add(time.Minute)) || time.Since(at) > time.Duration(backlogHours)*time.Hour {
		item.Status, item.Reason = "unavailable", "原消息超出当前历史处理范围"
		return item, nil
	}
	// Only local, unsubmitted plans are retired. Activity and audit evidence
	// remain; the fresh turn reads the original native message again.
	oldPlans, oldDeliveries := w.runtime.Plans, w.runtime.Deliveries
	plans := make([]*utterancePlan, 0, len(oldPlans))
	for _, plan := range oldPlans {
		if plan == nil || plan.Input.Thread != threadID || plan.Input.TraceID != messageID {
			plans = append(plans, plan)
		}
	}
	deliveries := make([]*delivery, 0, len(oldDeliveries))
	for _, d := range oldDeliveries {
		if d == nil || d.Thread != threadID || d.Trace != messageID {
			deliveries = append(deliveries, d)
		}
	}
	w.runtime.Plans, w.runtime.Deliveries = plans, deliveries
	if err := w.saveRuntime(); err != nil {
		w.runtime.Plans, w.runtime.Deliveries = oldPlans, oldDeliveries
		return ReplayItem{}, err
	}
	report, err := w.replayMessages(ctx, []string{messageID})
	if err != nil {
		return ReplayItem{}, err
	}
	if len(report.Items) != 1 || report.Items[0].Status != "queued" {
		if len(report.Items) == 1 {
			return ReplayItem{MessageID: messageID, Status: "unavailable", Reason: report.Items[0].Reason}, nil
		}
		return ReplayItem{}, errors.New("bot_replay_result_invalid")
	}
	w.activity(Record{Event: "round_manual_processing_queued", Category: "decision", ThreadID: threadID, MessageID: messageID, Status: "queued", Origin: "admin"})
	return ReplayItem{MessageID: messageID, Status: "queued", Reason: "原消息已交给 Agent 重新处理"}, nil
}

func (w *Worker) replayMessages(ctx context.Context, ids []string) (ReplayReport, error) {
	if w.runtime == nil {
		return ReplayReport{}, errors.New("bot_replay_not_ready")
	}
	seen := map[string]bool{}
	report := ReplayReport{Items: make([]ReplayItem, 0, len(ids))}
	type candidate struct {
		item *inboxItem
		msg  linuxnative.Message
	}
	candidates := make([]candidate, 0, len(ids))
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		if id == "" || seen[id] {
			return ReplayReport{}, errors.New("bot_replay_message_id_invalid")
		}
		seen[id] = true
		message, err := w.source.Message(ctx, id)
		if err != nil || message.ID != id || message.Direction != "incoming" {
			report.Items = append(report.Items, ReplayItem{MessageID: id, Status: "skipped", Reason: "原生消息不存在或不是收到的消息"})
			continue
		}
		if w.roundClosed(message.ThreadID, id) {
			report.Items = append(report.Items, ReplayItem{MessageID: id, Status: "skipped", Reason: "执行回合已由管理员关闭"})
			continue
		}
		if !w.allowed[message.ThreadID] {
			report.Items = append(report.Items, ReplayItem{MessageID: id, Status: "skipped", Reason: "会话未在当前 Agent 允许范围"})
			continue
		}
		at, parseErr := time.Parse(time.RFC3339Nano, message.CreatedAt)
		backlogHours := 24
		if w.config.Options != nil && w.config.Options.BacklogHours > 0 {
			backlogHours = w.config.Options.BacklogHours
		}
		if parseErr != nil || at.After(time.Now().Add(time.Minute)) || time.Since(at) > time.Duration(backlogHours)*time.Hour {
			report.Items = append(report.Items, ReplayItem{MessageID: id, Status: "skipped", Reason: "超出当前允许的历史回看时间"})
			continue
		}
		var found *inboxItem
		for i := range w.runtime.Inbox {
			if w.runtime.Inbox[i].Event.Data.MessageID == id {
				found = &w.runtime.Inbox[i]
				break
			}
		}
		if found == nil {
			report.Items = append(report.Items, ReplayItem{MessageID: id, Status: "skipped", Reason: "当前 Agent 队列中没有该原消息"})
			continue
		}
		if found.ToolStarted || replayHasEffects(w.runtime, id) {
			report.Items = append(report.Items, ReplayItem{MessageID: id, Status: "skipped", Reason: "原回合已有工具或投递记录，不重复执行"})
			continue
		}
		candidates = append(candidates, candidate{item: found, msg: message})
	}
	for _, c := range candidates {
		c.item.ReplayNonce = replayNonce("share-read-replay", c.msg.ID, time.Now().UTC().Format(time.RFC3339Nano))
		c.item.Phase = "received"
		c.item.Backlog = true
		c.item.BacklogParent = ""
		c.item.FollowupParent = ""
		c.item.FollowupConsumed = false
		c.item.OwnerModelReview = false
		c.item.ReceivedRecorded = false
		c.item.AggregateUntil = time.Time{}
		c.item.CollectedAt = time.Time{}
		c.item.RetryAfter = time.Time{}
		c.item.Updated = time.Now().UTC()
		w.activity(Record{Event: "message_replay_queued", Category: "decision", ThreadID: c.msg.ThreadID, Sender: c.msg.SenderID, MessageID: c.msg.ID, Reason: "administrator_replay_of_original_native_message", Status: "queued", Origin: "admin"})
		report.Items = append(report.Items, ReplayItem{MessageID: c.msg.ID, Status: "queued", Reason: "原消息已重新纳入 Agent 处理队列"})
	}
	if len(candidates) == 0 {
		return report, nil
	}
	if err := w.saveRuntime(); err != nil {
		return report, err
	}
	if err := w.drainInbox(ctx); err != nil {
		return report, err
	}
	return report, nil
}

func replayNonce(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func replayHasEffects(runtime *runtimeState, id string) bool {
	for _, plan := range runtime.Plans {
		if plan != nil && plan.Input.TraceID == id {
			return true
		}
	}
	for _, delivery := range runtime.Deliveries {
		if delivery != nil && delivery.Trace == id {
			return true
		}
	}
	for _, media := range runtime.Media {
		if media != nil && media.Trace == id {
			return true
		}
	}
	for _, reply := range runtime.MediaReplies {
		if reply != nil && reply.Trace == id {
			return true
		}
	}
	return false
}
