package admin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

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
		if t.State != "cancelled" && (t.State != "completed" || t.Delivery != nil && t.Delivery.State != "accepted") {
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
