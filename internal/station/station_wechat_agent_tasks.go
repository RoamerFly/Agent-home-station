package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/hostfile"
	"wechat-bot/internal/mcptools"
)

// stationAgentTaskRecord is the portable subset of the legacy agent-state
// task that the Station worker can own without importing the old admin
// manager. Tool checkpoints and delivery evidence stay in the normal queue;
// this file only schedules the next model continuation.
type stationAgentTaskRecord struct {
	Version     int               `json:"version"`
	ID          string            `json:"id"`
	Key         string            `json:"key,omitempty"`
	Account     string            `json:"account"`
	Thread      string            `json:"thread"`
	Sender      string            `json:"sender"`
	Title       string            `json:"title,omitempty"`
	Checkpoint  string            `json:"checkpoint"`
	State       string            `json:"state"`
	Next        time.Time         `json:"next"`
	Updated     time.Time         `json:"updated"`
	OriginTrace string            `json:"origin_trace,omitempty"`
	Runs        int               `json:"runs,omitempty"`
	Delivery    *bot.TaskDelivery `json:"delivery,omitempty"`
}

type stationAgentTaskFile struct {
	Version int                      `json:"version"`
	Tasks   []stationAgentTaskRecord `json:"tasks"`
}

const stationAgentTaskFileName = "agent-tasks.json"

func (w *stationWechatWorker) agentTaskPath() string {
	return filepath.Join(w.queueDir(), stationAgentTaskFileName)
}

func (w *stationWechatWorker) readAgentTasks() ([]stationAgentTaskRecord, error) {
	raw, err := os.ReadFile(w.agentTaskPath())
	if errors.Is(err, os.ErrNotExist) {
		return []stationAgentTaskRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var file stationAgentTaskFile
	if json.Unmarshal(raw, &file) != nil || file.Version != 1 || len(file.Tasks) > 512 {
		return nil, errors.New("微信持久任务账本格式无效")
	}
	for _, task := range file.Tasks {
		if task.ID == "" || task.Account == "" || task.Thread == "" || task.Sender == "" || task.Checkpoint == "" || !validStationAgentTaskState(task.State) {
			return nil, errors.New("微信持久任务身份或状态无效")
		}
	}
	return file.Tasks, nil
}

func (w *stationWechatWorker) agentTasksFor(account, thread, sender string) []stationAgentTaskRecord {
	w.ownerMu.Lock()
	defer w.ownerMu.Unlock()
	tasks, err := w.readAgentTasks()
	if err != nil {
		return nil
	}
	filtered := make([]stationAgentTaskRecord, 0, len(tasks))
	for _, task := range tasks {
		if task.Account == account && task.Thread == thread && task.Sender == sender {
			filtered = append(filtered, task)
		}
	}
	return filtered
}

func validStationAgentTaskState(state string) bool {
	switch state {
	case "pending", "waiting", "running", "completed", "cancelled", "needs_review", "failed":
		return true
	default:
		return false
	}
}

func (w *stationWechatWorker) writeAgentTasks(tasks []stationAgentTaskRecord) error {
	if len(tasks) > 512 {
		return errors.New("微信持久任务数量超过本机限制")
	}
	if err := os.MkdirAll(w.queueDir(), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(stationAgentTaskFile{Version: 1, Tasks: tasks})
	if err != nil {
		return err
	}
	return hostfile.WritePrivate(w.agentTaskPath(), raw)
}

func stationAgentTaskID(account, thread, sender, key string) string {
	h := sha256.Sum256([]byte(account + "\x00" + thread + "\x00" + sender + "\x00" + key))
	return "task-" + hex.EncodeToString(h[:])[:32]
}

func (w *stationWechatWorker) ownerTaskAllowed(in companion.Input) bool {
	if w == nil || in.Channel != "wechat" || !in.Directed || in.Preview || in.Surf || in.ReportOnly || in.Sender == "" {
		return false
	}
	if w.host == nil || w.host.ownerReader == nil {
		return false
	}
	return !w.policy.Grants[in.Thread].IsZero() && w.host.ownerBoundToWechat(w.connectionID, in.Account, in)
}

func (w *stationWechatWorker) agentTaskToolAccess(in companion.Input) bool {
	return w.ownerTaskAllowed(in)
}

func (t *stationAttachmentTools) agentTaskToolAccess(in companion.Input) bool {
	if t == nil || t.crossHost == nil {
		return false
	}
	w, err := t.crossHost.worker(t.crossConnection, false)
	return err == nil && w != nil && w.agentTaskToolAccess(in)
}

func stationAgentTaskTools() []mcptools.Tool {
	return []mcptools.Tool{
		mcptools.NewTool("agent_tasks", "只读查看当前微信会话的持久任务和检查点，不会创建、重试或执行任务。", `{"type":"object","properties":{},"additionalProperties":false}`),
		mcptools.NewTool("agent_task", "保存或更新当前微信会话的持久任务检查点。任务会在重启后继续；state=waiting/pending 只保存续办，不代表已完成或已送达。", `{"type":"object","properties":{"id":{"type":"string"},"key":{"type":"string"},"title":{"type":"string"},"checkpoint":{"type":"string"},"state":{"type":"string","enum":["pending","waiting","completed","cancelled","needs_review"]},"delay_seconds":{"type":"integer","minimum":0},"source_thread_id":{"type":"string"},"source_message_ids":{"type":"array","items":{"type":"string"},"maxItems":50}},"required":["checkpoint","state"],"additionalProperties":false}`),
	}
}

func (t *stationAttachmentTools) callAgentTaskTool(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if !t.agentTaskToolAccess(t.trace) {
		return "", apphost.ErrObservationForbidden
	}
	w, err := t.crossHost.worker(t.crossConnection, false)
	if err != nil || w == nil {
		return "", apphost.ErrUnavailable
	}
	w.ownerMu.Lock()
	defer w.ownerMu.Unlock()
	tasks, err := w.readAgentTasks()
	if err != nil {
		return "", err
	}
	if name == "agent_tasks" {
		out := []stationAgentTaskRecord{}
		for _, task := range tasks {
			if task.Account == t.trace.Account && task.Thread == t.trace.Thread && task.Sender == t.trace.Sender {
				out = append(out, task)
			}
		}
		data, _ := json.Marshal(map[string]any{"tasks": out, "read_only": true})
		return string(data), nil
	}
	var input struct {
		ID               string   `json:"id"`
		Key              string   `json:"key"`
		Title            string   `json:"title"`
		Checkpoint       string   `json:"checkpoint"`
		State            string   `json:"state"`
		DelaySeconds     int      `json:"delay_seconds"`
		SourceThreadID   string   `json:"source_thread_id"`
		SourceMessageIDs []string `json:"source_message_ids"`
	}
	if err := strictLocalMemoryDecode(raw, &input); err != nil || strings.TrimSpace(input.Checkpoint) == "" || input.State != "pending" && input.State != "waiting" && input.State != "completed" && input.State != "cancelled" && input.State != "needs_review" || input.DelaySeconds < 0 || input.DelaySeconds > 86400*30 {
		return "", errors.New("持久任务参数无效")
	}
	if input.ID != "" && input.Key != "" {
		return "", errors.New("任务不能同时指定 id 和 key")
	}
	index := -1
	if input.ID != "" {
		for i := range tasks {
			if tasks[i].ID == input.ID {
				index = i
				break
			}
		}
	} else if input.Key != "" {
		for i := range tasks {
			if tasks[i].Key == input.Key && tasks[i].Account == t.trace.Account && tasks[i].Thread == t.trace.Thread && tasks[i].Sender == t.trace.Sender {
				index = i
				break
			}
		}
	}
	if input.ID != "" && index < 0 {
		return "", apphost.ErrNotFound
	}
	now := time.Now().UTC()
	if index < 0 {
		if input.Key == "" {
			input.Key = input.Title + "-" + input.Checkpoint

		}
		id := stationAgentTaskID(t.trace.Account, t.trace.Thread, t.trace.Sender, input.Key)
		for _, task := range tasks {
			if task.ID == id {
				index = -1
				return "", errors.New("持久任务编号冲突，请更换 key")
			}
		}
		tasks = append(tasks, stationAgentTaskRecord{Version: 1, ID: id, Key: input.Key, Account: t.trace.Account, Thread: t.trace.Thread, Sender: t.trace.Sender, Title: input.Title, Checkpoint: input.Checkpoint, State: input.State, Next: now.Add(time.Duration(input.DelaySeconds) * time.Second), Updated: now, OriginTrace: t.trace.TraceID})
		index = len(tasks) - 1
	} else {
		task := &tasks[index]
		if task.Account != t.trace.Account || task.Thread != t.trace.Thread || task.Sender != t.trace.Sender {
			return "", apphost.ErrObservationForbidden
		}
		task.Checkpoint, task.State, task.Updated = input.Checkpoint, input.State, now
		task.Title = input.Title
		task.Next = now.Add(time.Duration(input.DelaySeconds) * time.Second)
	}
	if err := w.writeAgentTasks(tasks); err != nil {
		return "", err
	}
	data, _ := json.Marshal(map[string]any{"task": tasks[index], "saved": true, "delivery": "由微信回执单独确认"})
	return string(data), nil
}

func (w *stationWechatWorker) claimAgentTask(now time.Time, account string, busy func(string) bool) (*companion.Input, error) {
	w.ownerMu.Lock()
	tasks, err := w.readAgentTasks()
	w.ownerMu.Unlock()
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		task := &tasks[i]
		if task.Account != account || task.State != "pending" && task.State != "waiting" || time.Now().Before(task.Next) || busy != nil && busy(task.Thread) {
			continue
		}
		if w.host == nil || w.host.ownerReader == nil || w.policy.Grants[task.Thread].IsZero() {
			continue
		}
		if !w.host.ownerBoundToWechat(w.connectionID, task.Account, companion.Input{Channel: "wechat", Account: task.Account, Thread: task.Thread, Sender: task.Sender, Directed: true}) {
			continue
		}
		w.ownerMu.Lock()
		latest, latestErr := w.readAgentTasks()
		if latestErr != nil {
			w.ownerMu.Unlock()
			return nil, latestErr
		}
		for j := range latest {
			if latest[j].ID != task.ID || latest[j].State != "pending" && latest[j].State != "waiting" || time.Now().Before(latest[j].Next) {
				continue
			}
			latest[j].State, latest[j].Runs, latest[j].Updated = "running", latest[j].Runs+1, now.UTC()
			if writeErr := w.writeAgentTasks(latest); writeErr != nil {
				w.ownerMu.Unlock()
				return nil, writeErr
			}
			claimed := latest[j]
			w.ownerMu.Unlock()
			return &companion.Input{Channel: "wechat", Account: claimed.Account, Thread: claimed.Thread, Sender: claimed.Sender, Mode: "direct", Directed: true, IsGroup: strings.HasSuffix(claimed.Thread, "@chatroom"), BackgroundResult: true, TaskID: claimed.ID, OriginTraceID: claimed.OriginTrace, TraceID: claimed.ID + "-run-" + now.UTC().Format("20060102T150405"), Text: "继续已保存任务：" + claimed.Checkpoint, MessageTime: now.UTC().Format(time.RFC3339Nano)}, nil
		}
		w.ownerMu.Unlock()
	}
	return nil, nil
}

func (w *stationWechatWorker) updateAgentTask(in companion.Input, state string, delivery *bot.TaskDelivery, retry time.Duration, reason string) error {
	w.ownerMu.Lock()
	defer w.ownerMu.Unlock()
	tasks, err := w.readAgentTasks()
	if err != nil {
		return err
	}
	for i := range tasks {
		if tasks[i].ID != in.TaskID || tasks[i].Account != in.Account || tasks[i].Thread != in.Thread {
			continue
		}
		task := &tasks[i]
		if delivery != nil {
			copy := *delivery
			task.Delivery = &copy
		}
		if retry > 0 {
			task.State, task.Next = "waiting", time.Now().UTC().Add(retry)
		} else if state == "yielded_to_message" {
			task.State, task.Next = "waiting", time.Now().UTC().Add(bot.DeliveryRetryBackoff(task.Runs))
		} else if state == "retrying_model" {
			task.State = "waiting"
		} else if task.State == "running" {
			if state == "needs_review" || state == "failed" {
				task.State = "needs_review"
			} else if state != "silent" {
				task.State = "completed"
			}
		}
		if reason != "" {
			task.Checkpoint = strings.TrimSpace(task.Checkpoint + "；" + reason)
		}
		task.Updated = time.Now().UTC()
		return w.writeAgentTasks(tasks)
	}
	return nil
}

func (w *stationWechatWorker) agentTaskActive(in companion.Input) bool {
	if in.TaskID == "" {
		return true
	}
	w.ownerMu.Lock()
	defer w.ownerMu.Unlock()
	tasks, err := w.readAgentTasks()
	if err != nil {
		return false
	}
	for _, task := range tasks {
		if task.ID == in.TaskID && task.Account == in.Account && task.Thread == in.Thread {
			return task.State != "completed" && task.State != "cancelled" && task.State != "needs_review" && task.State != "failed"
		}
	}
	return false
}
