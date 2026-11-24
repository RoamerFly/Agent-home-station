package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A scheduled task execution uses a new trace on each attempt. The stable
// task ID is the part before the timestamp, and its persisted state is the
// authority for the execution-round card after the last log entry.
func continuationTaskID(trace string) string {
	if strings.HasPrefix(trace, "admin-owner-request-") && len(trace) == len("admin-owner-request-")+64 {
		return strings.TrimPrefix(trace, "admin-owner-request-")
	}
	if strings.HasPrefix(trace, "music-result-") {
		cut := strings.LastIndexByte(trace, '-')
		if cut > len("music-result-") && len(trace[len("music-result-"):cut]) == 64 {
			return trace[len("music-result-"):cut]
		}
	}
	if !strings.HasPrefix(trace, "task-") || len(trace) > 512 {
		return ""
	}
	cut := strings.LastIndexByte(trace, '-')
	if cut <= len("task-") || cut == len(trace)-1 {
		return ""
	}
	if _, err := time.Parse("20060102T150405.000000000", trace[cut+1:]); err != nil {
		return ""
	}
	return trace[len("task-"):cut]
}

type logTaskState struct {
	State   string    `json:"state"`
	Updated time.Time `json:"updated"`
	Active  bool      `json:"active,omitempty"`
}

func unfinishedExecutionTask(state string) bool {
	return state == "pending" || state == "waiting" || state == "running" || state == "waiting_delivery"
}

type logInboxState struct {
	Phase string `json:"phase"`
}

type logPlanState struct {
	State      string `json:"state"`
	StopReason string `json:"stop_reason,omitempty"`
	Index      int    `json:"index"`
	Total      int    `json:"total"`
	Created    string `json:"created,omitempty"`
}

// The inbox checkpoint is the source of truth for old queued cards. Return
// only phases and plan counts; message bodies and native events stay private.
func (s *Server) logRuntimeStates() (map[string]logInboxState, map[string]logPlanState, bool) {
	data, err := os.ReadFile(filepath.Join(s.manager.stateDir, "messages-v2.json"))
	if err != nil {
		return nil, nil, false
	}
	var snapshot struct {
		Inbox []struct {
			Phase string `json:"phase"`
			Event struct {
				Data struct {
					MessageID string `json:"messageId"`
				} `json:"data"`
			} `json:"event"`
		} `json:"inbox"`
		Plans []struct {
			Trace      string            `json:"trace"`
			State      string            `json:"state"`
			StopReason string            `json:"stop_reason"`
			Index      int               `json:"index"`
			Created    string            `json:"created"`
			Messages   []json.RawMessage `json:"messages"`
			Input      struct {
				Thread string `json:"thread"`
			} `json:"input"`
		} `json:"utterance_plans"`
	}
	if json.Unmarshal(data, &snapshot) != nil {
		return nil, nil, false
	}
	states := make(map[string]logInboxState, len(snapshot.Inbox))
	for _, item := range snapshot.Inbox {
		if item.Event.Data.MessageID != "" {
			states[item.Event.Data.MessageID] = logInboxState{Phase: item.Phase}
		}
	}
	plans := make(map[string]logPlanState, len(snapshot.Plans))
	for _, plan := range snapshot.Plans {
		if plan.Input.Thread == "" || plan.Trace == "" || plan.Index < 0 || plan.Index > len(plan.Messages) {
			continue
		}
		plans[plan.Input.Thread+"\x00"+plan.Trace] = logPlanState{State: plan.State, StopReason: plan.StopReason, Index: plan.Index, Total: len(plan.Messages), Created: plan.Created}
	}
	return states, plans, true
}

func (s *Server) logInboxStates() (map[string]logInboxState, bool) {
	inbox, _, loaded := s.logRuntimeStates()
	return inbox, loaded
}

func (s *Server) logTaskStates(items []LogEntry) map[string]logTaskState {
	states := map[string]logTaskState{}
	account := s.manager.native.Account()
	reminders := map[string]agentTask{}
	origins := map[string]agentTask{}
	tasks := map[string]agentTask{}
	for _, task := range s.manager.agent.list(account, "") {
		tasks[task.ID] = task
		if task.Reminder != nil && task.OriginTrace != "" {
			key := task.Thread + "\x00" + task.OriginTrace
			if old, exists := reminders[key]; !exists || newerRoundTask(task, old) {
				reminders[key] = task
			}
		} else if task.OriginTrace != "" {
			thread := task.RequestThread
			if thread == "" {
				thread = task.Thread
			}
			key := thread + "\x00" + task.OriginTrace
			if old, exists := origins[key]; !exists || newerRoundTask(task, old) {
				origins[key] = task
			}
		}
	}
	for _, item := range items {
		if item.ThreadID != "" && item.MessageID != "" {
			key := item.ThreadID + "\x00" + item.MessageID
			if task, ok := origins[key]; ok {
				states[key] = logTaskState{State: task.State, Updated: task.Updated, Active: unfinishedExecutionTask(task.State)}
			}
		}
		// A later continuation can be the only activity on the selected day.
		// Its explicit ownership edge still needs the original task outcome;
		// quoted or merely related messages are not ownership edges.
		if roundOwnershipEvent(item.Event) && item.ThreadID != "" && item.RelatedMessageID != "" {
			key := item.ThreadID + "\x00" + item.RelatedMessageID
			if task, ok := origins[key]; ok {
				states[key] = logTaskState{State: task.State, Updated: task.Updated, Active: unfinishedExecutionTask(task.State)}
			}
		}
		if item.Event == "reminder_due" && item.MessageID != "" {
			if reminder, ok := reminders[item.ThreadID+"\x00"+item.MessageID]; ok {
				state := reminder.State
				updated := reminder.Updated
				if follow, ok := tasks[reminder.Reminder.FollowUpTaskID]; ok {
					state, updated = follow.State, follow.Updated
				}
				states[item.ThreadID+"\x00"+item.MessageID] = logTaskState{State: state, Updated: updated, Active: unfinishedExecutionTask(state)}
			}
		}
		id := continuationTaskID(item.MessageID)
		if id == "" || item.ThreadID == "" {
			continue
		}
		key := item.ThreadID + "\x00" + id
		if _, seen := states[key]; seen {
			continue
		}
		if task, ok := s.manager.agent.lookupTask(id, account, item.ThreadID); ok {
			states[key] = logTaskState{State: task.State, Updated: task.Updated}
		}
	}
	// Console cross-media sends have durable receipts but no Agent task. Join
	// their stable request identity so a completed batch leaves the pending tab.
	files, err := os.ReadDir(filepath.Join(s.manager.dir, "cross-media"))
	if err == nil {
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(s.manager.dir, "cross-media", file.Name()))
			var batch crossMediaBatch
			if err != nil || json.Unmarshal(b, &batch) != nil || batch.Account != account || batch.RequestIdentity == "" || batch.Target == "" || validateCrossBatch(batch, account) != nil {
				continue
			}
			state := "waiting"
			if crossBatchAccepted(batch) {
				state = "completed"
			} else if !batch.CancelledAt.IsZero() || batch.SupersededBy != "" {
				state = "cancelled"
			}
			states[batch.Target+"\x00"+batch.RequestIdentity] = logTaskState{State: state, Updated: batch.Created}
		}
	}
	return states
}

func roundOwnershipEvent(event string) bool {
	switch event {
	case "generation_followup_consumed", "generation_followup_settled", "backlog_message_grouped", "background_turn_yielded", "message_aggregated", "task_followup_attached":
		return true
	}
	return false
}

// Duplicate continuations can share one original message. The latest saved
// outcome wins; an older pending task must not revive a completed round.
func newerRoundTask(candidate, old agentTask) bool {
	return candidate.Updated.After(old.Updated) || candidate.Updated.Equal(old.Updated) &&
		!unfinishedExecutionTask(candidate.State) && unfinishedExecutionTask(old.State)
}

// Some cross-conversation sends were queued in the activity journal before
// their native receipt arrived, and no later activity row was recorded. Read
// the persisted native send by its exact ID; this never submits or retries it.
func (s *Server) logSendStates(ctx context.Context, items []LogEntry) map[string]bool {
	accepted := map[string]bool{}
	native, ok := s.manager.native.(nativeTools)
	if !ok {
		return accepted
	}
	ids := map[string]string{}
	for _, item := range items {
		if (item.Event == "agent_cross_send" || item.Event == "cross_conversation_reply_queued") &&
			item.Status != "accepted" && item.ThreadID != "" && item.SendID != "" {
			ids[item.ThreadID+"\x00"+item.SendID] = item.SendID
		}
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	for key, id := range ids {
		status, err := native.ToolSendStatus(lookupCtx, id)
		if err != nil || status.ID != id || status.ThreadID+"\x00"+id != key || status.State != "accepted" || status.ServerID == "" {
			continue
		}
		accepted[key] = true
	}
	return accepted
}

// Pages traverse immutable archive batches, never load all archived bodies into
// memory. The UI follows continuation positions automatically for a chosen day.
func (s *Server) logsByDate(w http.ResponseWriter, r *http.Request) {
	day := r.URL.Query().Get("date")
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	start, err := time.ParseInLocation("2006-01-02", day, zone)
	if err != nil || start.Format("2006-01-02") != day {
		fail(w, 400, "请选择有效日期")
		return
	}
	end := start.AddDate(0, 0, 1)
	after := r.URL.Query().Get("after")
	batches, next, err := s.manager.historyArchive().List("activity", activityScope(), after, 10)
	if err != nil {
		fail(w, 400, "历史记录读取位置无效或暂不可用，请刷新重试")
		return
	}
	items := []LogEntry{}
	seen := map[string]bool{}
	add := func(entries []LogEntry) {
		for _, entry := range entries {
			stamp, e := time.Parse(time.RFC3339Nano, entry.Time)
			if e != nil || stamp.Before(start) || !stamp.Before(end) {
				continue
			}
			if entry.ID != "" && seen[entry.ID] {
				continue
			}
			seen[entry.ID] = true
			if entry.Category == "" {
				entry.Category = eventCategory(entry.Event)
			}
			if entry.Event == "message_received" {
				entry.Category = "received"
			}
			items = append(items, entry)
		}
	}
	if after == "" {
		add(s.manager.Logs())
	}
	for _, batch := range batches {
		if r.Context().Err() != nil {
			return
		}
		entries, e := s.manager.archiveLogs(batch.ID)
		if e != nil {
			fail(w, 500, "部分历史记录未能读取，请刷新重试；原始记录保留")
			return
		}
		add(entries)
	}
	inbox, plans, loaded := s.logRuntimeStates()
	respond(w, 200, map[string]any{"items": s.manager.nameLogEntries(r.Context(), items), "task_states": s.logTaskStates(items), "send_states": s.logSendStates(r.Context(), items), "inbox_states": inbox, "plan_states": plans, "inbox_loaded": loaded, "date": day, "next": next, "complete": next == "", "archive_batches": len(batches)})
}
