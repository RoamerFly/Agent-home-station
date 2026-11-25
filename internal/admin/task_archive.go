package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
		a.archived[task.ID] = task
	}
	return nil
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
