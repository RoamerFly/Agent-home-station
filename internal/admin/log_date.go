package admin

import (
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
}

func (s *Server) logTaskStates(items []LogEntry) map[string]logTaskState {
	states := map[string]logTaskState{}
	account := s.manager.native.Account()
	for _, item := range items {
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
	respond(w, 200, map[string]any{"items": s.manager.nameLogEntries(r.Context(), items), "task_states": s.logTaskStates(items), "date": day, "next": next, "complete": next == "", "archive_batches": len(batches)})
}
