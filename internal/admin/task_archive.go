package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func historicalReminderSettled(t agentTask) bool {
	// A cancelled reminder will not resume or retry. The administrator can
	// remove it even when an earlier delivery receipt was never confirmed.
	if t.State == "cancelled" {
		return true
	}
	terminal := t.State == "completed" || t.State == "stopped" || t.State == "expired"
	return terminal && len(t.PendingOperations) == 0 && (t.Delivery == nil || t.Delivery.State == "accepted")
}

// deleteHistoricalReminder removes an ended reminder and its ended handoff.
// Files are moved out of the archive first, so a failed state write can put
// them back without losing the original task contents.
func (a *agentControl) deleteHistoricalReminder(id, account string) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.data.Tasks[id]
	if !ok {
		t, ok = a.archived[id]
	}
	if !ok || t.Account != account || t.Reminder == nil && !t.ReminderDue {
		return nil, errors.New("未找到提醒任务")
	}
	if !historicalReminderSettled(t) {
		return nil, errors.New("提醒任务尚未结束或仍有待核对的发送结果")
	}
	selected := map[string]agentTask{id: t}
	_, isActive := a.data.Tasks[id]
	active := map[string]bool{id: isActive}
	for _, child := range a.data.Tasks {
		if child.ParentID == id && child.Account == account {
			if !historicalReminderSettled(child) {
				return nil, errors.New("提醒的后续任务尚未结束")
			}
			if child.ReminderDue {
				selected[child.ID] = child
				active[child.ID] = true
			}
		}
	}
	for _, child := range a.archived {
		if child.ParentID == id && child.Account == account {
			if !historicalReminderSettled(child) {
				return nil, errors.New("提醒的后续任务尚未结束")
			}
			if child.ReminderDue {
				if _, present := selected[child.ID]; !present {
					selected[child.ID] = child
					active[child.ID] = false
				}
			}
		}
	}
	type movedFile struct{ from, to string }
	moved := []movedFile{}
	restore := func() {
		for i := len(moved) - 1; i >= 0; i-- {
			_ = os.Rename(moved[i].to, moved[i].from)
		}
	}
	for taskID, task := range selected {
		paths := []string{}
		if _, archived := a.archived[taskID]; archived {
			paths = append(paths, filepath.Join(a.dir, "archive", "agent-tasks", agentID(taskID)+".json"))
		}
		if task.Reminder != nil {
			paths = append(paths, filepath.Join(a.dir, "reminders", taskID+".json"))
		}
		for _, path := range paths {
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				restore()
				return nil, err
			}
			staged := path + ".deleting"
			if _, err := os.Lstat(staged); !errors.Is(err, os.ErrNotExist) {
				restore()
				return nil, fmt.Errorf("历史文件待清理: %s", filepath.Base(path))
			}
			if err := os.Rename(path, staged); err != nil {
				restore()
				return nil, err
			}
			moved = append(moved, movedFile{path, staged})
		}
	}
	for taskID := range selected {
		delete(a.data.Tasks, taskID)
		delete(a.archived, taskID)
	}
	if err := a.persist(); err != nil {
		for taskID, task := range selected {
			if active[taskID] {
				a.data.Tasks[taskID] = task
			} else {
				a.archived[taskID] = task
			}
		}
		restore()
		return nil, err
	}
	ids := make([]string, 0, len(selected))
	for taskID := range selected {
		ids = append(ids, taskID)
	}
	sort.Strings(ids)
	for _, file := range moved {
		_ = os.Remove(file.to)
	}
	return ids, nil
}

func (a *agentControl) loadTaskArchive() error {
	a.archived = map[string]agentTask{}
	entries, err := os.ReadDir(filepath.Join(a.dir, "archive", "agent-tasks"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, e := os.ReadFile(filepath.Join(a.dir, "archive", "agent-tasks", entry.Name()))
		var task agentTask
		if e != nil || json.Unmarshal(b, &task) != nil || entry.Name() != agentID(task.ID)+".json" {
			return errors.New("任务历史不可读，未覆盖")
		}
		// A restart after persisting a reopened task but before removing its
		// archive file must use the active copy of that same task ID.
		if _, active := a.data.Tasks[task.ID]; active {
			continue
		}
		a.archived[task.ID] = task
	}
	return nil
}

// reopenArchivedTask is only called by the authenticated administrator route.
// Persist the original task ID in the active store before removing its archive
// copy; the active copy wins if a restart interrupts that cleanup.
func (a *agentControl) reopenArchivedTask(id, account, continuationNote string) (agentTask, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, active := a.data.Tasks[id]; active {
		return agentTask{}, errors.New("任务仍在活跃队列")
	}
	t, ok := a.archived[id]
	if !ok || t.Account != account || t.State != "completed" || t.Reminder != nil || t.ReminderDue {
		return agentTask{}, errors.New("未找到可恢复的已完成任务")
	}
	if len(t.PendingOperations) > 0 || t.resultKind() != "" || t.DeliveryJob != "" || t.RecoveryMessageID != "" || t.Delivery != nil && t.Delivery.State != "accepted" {
		return agentTask{}, errors.New("原任务仍有待核对的工具或发送结果，请先处理原记录")
	}
	note := strings.TrimSpace(continuationNote)
	if len([]rune(note)) > 600 || strings.ContainsRune(note, 0) {
		return agentTask{}, errors.New("续办说明最多 600 字且不能包含空字符")
	}
	if t.Delivery != nil && note == "" {
		return agentTask{}, errors.New("原任务已有发送回执，请说明尚需续做的部分")
	}
	now := time.Now().UTC()
	if note != "" {
		if t.Checkpoint != "" {
			t.Checkpoint += "\n"
		}
		t.Checkpoint += "管理员最新续办说明（取代此前无法继续的状态；原请求不变，" + now.Format(time.RFC3339) + "）：" + note
	}
	if t.Delivery != nil {
		// The existing receipt remains intact until the next claim. The claim
		// path clears accepted receipts to permit a new send, so retain its
		// exact values in the durable checkpoint for the continuation.
		b, _ := json.Marshal(t.Delivery)
		t.Checkpoint += "\n已接受发送回执（已发送部分勿重复发送）：" + string(b)
	}
	t.State = "pending"
	t.WorkCompleted = false
	t.Next = now
	t.Updated = now
	t.NoProgressKey = ""
	t.NoProgressRuns = 0
	t.NoProgressObservedRun = 0
	t.LastResult = "管理员明确恢复原任务，沿用原消息、已有回执和已创建的工具作业，仅续做未完成部分"
	a.data.Tasks[id] = copyTask(t)
	if err := a.persist(); err != nil {
		delete(a.data.Tasks, id)
		return agentTask{}, err
	}
	delete(a.archived, id)
	// Removal is cleanup only. If it fails, loadTaskArchive still prefers the
	// persisted active copy and a later retirement overwrites this archive.
	_ = os.Remove(filepath.Join(a.dir, "archive", "agent-tasks", agentID(id)+".json"))
	return copyTask(t), nil
}

// Reopen a completed task as a new correction generation. The caller has
// already verified the added native messages and prepared the replacement
// checkpoint; this method commits that snapshot before removing the archive.
func (a *agentControl) reopenArchivedCorrectedTask(t agentTask) (agentTask, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, active := a.data.Tasks[t.ID]; active {
		return agentTask{}, errors.New("任务仍在活跃队列")
	}
	old, ok := a.archived[t.ID]
	if !ok || old.Account != t.Account || old.Thread != t.Thread || old.Sender != t.Sender ||
		old.State != "completed" || !old.Updated.Equal(t.Updated) || t.CorrectionAt.IsZero() ||
		old.Reminder != nil || old.ReminderDue || len(old.PendingOperations) > 0 {
		return agentTask{}, errors.New("已完成任务状态已变化，无法纠错恢复")
	}
	t.State = "pending"
	t.WorkCompleted = false
	t.Delivery = nil
	t.Next = time.Now().UTC()
	t.Updated = t.Next
	t.Runs = 0
	a.data.Tasks[t.ID] = copyTask(t)
	if err := a.persist(); err != nil {
		delete(a.data.Tasks, t.ID)
		return agentTask{}, err
	}
	delete(a.archived, t.ID)
	_ = os.Remove(filepath.Join(a.dir, "archive", "agent-tasks", agentID(t.ID)+".json"))
	return copyTask(t), nil
}

// Completion and delivery remain separate. Incomplete/uncertain delivery never
// disappears from the active list merely because a model said "completed".
func (a *agentControl) retireSettled() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.retireSettledLocked()
}
func (a *agentControl) retireSettledLocked() error {
	if a.archived == nil {
		a.archived = map[string]agentTask{}
	}
	moved := map[string]agentTask{}
	for id, t := range a.data.Tasks {
		if len(t.PendingOperations) > 0 && t.State != "cancelled" {
			continue
		}
		if t.State != "cancelled" && ((t.State != "completed" && t.State != "stopped") || t.Delivery != nil && t.Delivery.State != "accepted") {
			continue
		}
		dir := filepath.Join(a.dir, "archive", "agent-tasks")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		b, err := json.Marshal(t)
		if err == nil {
			err = writePrivate(filepath.Join(dir, agentID(id)+".json"), b)
		}
		if err != nil {
			return err
		}
		moved[id] = t
	}
	if len(moved) == 0 {
		return nil
	}
	for id := range moved {
		delete(a.data.Tasks, id)
	}
	if err := a.persist(); err != nil {
		for id, t := range moved {
			a.data.Tasks[id] = t
		}
		return err
	}
	for id, t := range moved {
		a.archived[id] = t
	}
	return nil
}
func (a *agentControl) lookupTask(id, account, thread string) (agentTask, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.data.Tasks[id]
	if !ok {
		t, ok = a.archived[id]
	}
	if !ok || t.Account != account || thread != "" && t.Thread != thread {
		return agentTask{}, false
	}
	return copyTask(t), true
}
func (a *agentControl) taskHistory(account, thread string) []agentTask {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []agentTask{}
	for _, t := range a.archived {
		if t.Account == account && (thread == "" || t.Thread == thread) {
			out = append(out, copyTask(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}
