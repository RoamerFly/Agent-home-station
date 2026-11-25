package station

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/mcptools"
)

// stationWechatTaskStatusTool is deliberately read-only.  The legacy admin
// worker exposed a generic agent_tasks view, while the Station worker stores
// the durable inbox, reply plans, media jobs and delivery receipts in the
// queue checkpoint.  Exposing that checkpoint through the same WeChat tool
// gives the model and the owner one portable status view without inventing a
// second task database or replaying an unknown side effect.
const stationWechatTaskStatusTool = "wechat_task_status"

func stationWechatTaskStatusToolSpec() mcptools.Tool {
	return mcptools.NewTool(stationWechatTaskStatusTool,
		"只读查看当前微信连接的持久处理队列、执行回合、媒体任务和投递状态。返回的是已保存的状态，不会创建、重试或重放任务；结果未知时请沿用原任务编号继续核对。默认查看当前会话，可用 after 分页。",
		`{"type":"object","properties":{"thread_id":{"type":"string"},"after":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":100}},"additionalProperties":false}`)
}

func (t *stationAttachmentTools) taskCheckpointPath() (string, error) {
	// Current Station workers keep messages-v2.json below queue.  The other
	// candidates retain compatibility with an already-moved worker directory
	// and with small synthetic fixtures; all reads remain inside t.dir.
	for _, path := range []string{
		filepath.Join(t.dir, "queue", "messages-v2.json"),
		filepath.Join(t.dir, "runtime", "queue", "messages-v2.json"),
		filepath.Join(t.dir, "messages-v2.json"),
	} {
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() {
			return path, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", os.ErrNotExist
}

func (t *stationAttachmentTools) taskStatusInput(raw []byte) (thread, after string, limit int, err error) {
	var input struct {
		Thread string `json:"thread_id"`
		After  string `json:"after"`
		Limit  int    `json:"limit"`
	}
	if err = strictLocalMemoryDecode(raw, &input); err != nil {
		return "", "", 0, errors.New("微信任务状态参数无效")
	}
	thread = strings.TrimSpace(input.Thread)
	if thread == "" {
		thread = t.trace.Thread
	}
	if thread == "" || len(thread) > 256 || len(input.After) > 256 {
		return "", "", 0, errors.New("微信任务会话或分页游标无效")
	}
	limit = input.Limit
	if limit == 0 {
		limit = 40
	}
	if limit < 1 || limit > 100 {
		return "", "", 0, errors.New("微信任务页大小应为 1 到 100")
	}
	return thread, input.After, limit, nil
}

func (t *stationAttachmentTools) callTaskStatus(ctx context.Context, raw []byte) (string, error) {
	if !t.scopeAllowed(t.trace, t.reads) {
		return "", apphost.ErrObservationForbidden
	}
	if t.crossHost == nil {
		return "", apphost.ErrUnavailable
	}
	thread, after, limit, err := t.taskStatusInput(raw)
	if err != nil {
		return "", err
	}
	// A normal member can inspect only the currently authorized conversation;
	// cross-conversation reads still go through the existing owner proof tool.
	if thread != t.trace.Thread {
		return "", apphost.ErrObservationForbidden
	}
	worker, workerErr := t.crossHost.worker(t.crossConnection, false)
	if workerErr != nil || worker == nil {
		return "", apphost.ErrUnavailable
	}
	path, err := t.taskCheckpointPath()
	if errors.Is(err, os.ErrNotExist) {
		page := bot.TaskEvidencePage{Items: []bot.TaskEvidence{}, Limit: limit}
		tasks := worker.agentTasksFor(t.trace.Account, t.trace.Thread, t.trace.Sender)
		data, _ := json.Marshal(map[string]any{"page": page, "agent_tasks": tasks, "coverage": "station_queue_empty", "read_only": true})
		return string(data), nil
	}
	if err != nil {
		return "", errors.New("微信任务状态暂时不可读")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("微信任务状态暂时不可读")
	}
	defer file.Close()
	page, err := bot.ReadTaskEvidencePageFrom(file, t.native.Account(), thread, after, limit)
	if err != nil {
		return "", errors.New("微信任务账本身份或格式无法核对")
	}
	tasks := worker.agentTasksFor(t.trace.Account, t.trace.Thread, t.trace.Sender)
	data, err := json.Marshal(map[string]any{"page": page, "agent_tasks": tasks, "coverage": "station_queue_checkpoint", "read_only": true})
	return string(data), err
}
