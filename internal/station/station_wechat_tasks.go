package station

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/websession"
)

func (s *stationWechat) taskDetail(r *http.Request) (*stationWechatWorker, bot.TaskDetail, error) {
	id, key := r.PathValue("connection"), r.PathValue("task")
	if !identity.ValidPathID(id) || key == "" || len(key) > 8192 {
		return nil, bot.TaskDetail{}, errors.New("invalid task identity")
	}
	connection, err := s.stationWechatNative.Inspect(r.Context(), id)
	if err != nil || connection.ID != id || connection.AccountID == "" {
		return nil, bot.TaskDetail{}, errors.New("connection unavailable")
	}
	worker, err := s.worker(id, false)
	if err != nil || worker == nil {
		return nil, bot.TaskDetail{}, errors.New("task unavailable")
	}
	worker.deliveryMu.Lock()
	defer worker.deliveryMu.Unlock()
	detail, err := wechatapp.ReadWorkerTaskDetail(worker.runtimeStateDir(), connection.AccountID, key)
	return worker, detail, err
}

func (s *stationWechat) readTaskDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	worker, detail, err := s.taskDetail(r)
	if err != nil {
		websession.Fail(w, 404, "任务不存在或当前连接无法核对")
		return
	}
	sources := []wechatapp.AttachmentRequest{}
	worker.mu.Lock()
	for _, source := range detail.Sources {
		if !worker.policy.AttachmentGrants[source.Thread].IsZero() {
			sources = append(sources, wechatapp.AttachmentRequest{Thread: source.Thread, Message: source.MessageID})
		}
	}
	worker.mu.Unlock()
	websession.Respond(w, 200, map[string]any{"task": detail.Task, "documents": sources})
}

func (s *stationWechat) acquireTaskAttachment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Message string `json:"message_id"`
	}
	if !websession.Decode(w, r, &in) {
		return
	}
	worker, detail, err := s.taskDetail(r)
	if err != nil {
		websession.Fail(w, 404, "任务或关联原件无法核对")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	for _, source := range detail.Sources {
		if source.MessageID != in.Message {
			continue
		}
		request := wechatapp.AttachmentRequest{Thread: source.Thread, Message: source.MessageID}
		access, err := s.ownerAttachmentAccess(ctx, r.PathValue("connection"), worker, request)
		if err != nil {
			break
		}
		dataset, err := access.native.Identity(ctx)
		if err != nil || dataset != detail.Dataset {
			break
		}
		memory, err := companion.NewStore(filepath.Join(worker.dir, "memory"))
		if err != nil {
			break
		}
		defer memory.Close()
		access.ref, access.store = source, memory
		// A task may retain an exact quoted original that predates indexing.
		// Match documentAccess: native identity/hash plus the memory recall
		// fence establishes visibility for this specific source.
		access.history = source.EventID == ""
		result, err := wechatapp.AcquireAttachment(ctx, request, access)
		dataset, datasetErr := access.native.Identity(ctx)
		if err == nil && datasetErr == nil && dataset == detail.Dataset {
			websession.Respond(w, 200, result)
			return
		}
		break
	}
	websession.Fail(w, 404, "当前授权、任务来源或原件已变化，请重新核对")
}

func (s *stationWechat) readTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("connection")
	if !identity.ValidPathID(id) {
		websession.Fail(w, 400, "连接编号无效")
		return
	}
	query := r.URL.Query()
	limit := 40
	var err error
	if value := query.Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
	}
	thread, after := query.Get("thread"), query.Get("after")
	if err != nil || limit < 1 || len(thread) > 256 || len(after) > 1024 {
		websession.Fail(w, 400, "任务筛选或分页参数无效")
		return
	}
	connection, err := s.stationWechatNative.Inspect(r.Context(), id)
	if err != nil || connection.ID != id || connection.AccountID == "" {
		websession.Fail(w, 404, "无法核对当前微信连接")
		return
	}
	worker, err := s.worker(id, false)
	page := bot.TaskEvidencePage{Items: []bot.TaskEvidence{}, Limit: limit}
	if err == nil && worker != nil {
		worker.deliveryMu.Lock()
		page, err = wechatapp.ReadWorkerTaskPage(worker.runtimeStateDir(), connection.AccountID, thread, after, limit)
		worker.deliveryMu.Unlock()
	}
	if err != nil {
		websession.Fail(w, 503, "任务状态暂不可读，原记录保留")
		return
	}
	websession.Respond(w, 200, page)
}
