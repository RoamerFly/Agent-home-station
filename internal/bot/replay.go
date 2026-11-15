package bot

import (
	"context"
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
	ids  []string
	done chan replayResponse
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

func (w *Worker) drainReplayRequests(ctx context.Context) error {
	for {
		select {
		case request := <-w.replayRequests:
			report, err := w.replayMessages(ctx, request.ids)
			request.done <- replayResponse{report: report, err: err}
		case <-ctx.Done():
			return nil
		default:
			return nil
		}
	}
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
