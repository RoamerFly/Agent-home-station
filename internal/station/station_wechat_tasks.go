package station

import (
	"context"
	"errors"
	"io"
	"mime"
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

func (s *stationWechat) taskAttachmentAccess(ctx context.Context, id string, worker *stationWechatWorker, detail bot.TaskDetail, source *companion.SourceRef) (stationAttachmentAccess, *companion.Store, error) {
	request := wechatapp.AttachmentRequest{Thread: source.Thread, Message: source.MessageID}
	access, err := s.ownerAttachmentAccess(ctx, id, worker, request)
	if err != nil {
		return access, nil, err
	}
	dataset, err := access.native.Identity(ctx)
	if err != nil || dataset != detail.Dataset {
		return access, nil, wechatapp.ErrAttachmentDenied
	}
	memory, err := companion.NewStore(filepath.Join(worker.dir, "memory"))
	if err != nil {
		return access, nil, err
	}
	access.ref, access.store = source, memory
	// Preserve documentAccess's exact quoted originals that predate indexing.
	access.history = source.EventID == ""
	return access, memory, nil
}

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
		access, memory, err := s.taskAttachmentAccess(ctx, r.PathValue("connection"), worker, detail, source)
		if err != nil {
			break
		}
		defer memory.Close()
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

func (s *stationWechat) downloadTaskAttachment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	asset := r.PathValue("asset")
	if !wechatapp.ValidAttachmentAssetID(asset) {
		http.NotFound(w, r)
		return
	}
	worker, detail, err := s.taskDetail(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	for _, reference := range detail.Sources {
		access, memory, err := s.taskAttachmentAccess(ctx, r.PathValue("connection"), worker, detail, reference)
		if err != nil {
			continue
		}
		defer memory.Close()
		epoch, epochErr := memory.RecallEpoch(ctx)
		source, sourceErr := access.source(ctx, reference.Thread, reference.MessageID)
		if epochErr != nil || sourceErr != nil || source.Dataset != detail.Dataset || source.AssetID() != asset {
			continue
		}
		record, file, err := access.open(ctx, source)
		if err != nil {
			continue
		}
		defer file.Close()
		allowed := func() bool { return access.confirmObservationSource(ctx, source, epoch) == nil }
		if !allowed() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": source.Filename}))
		w.Header().Set("Content-Length", strconv.FormatInt(record.Bytes, 10))
		// Check source, grant and recall revision before each bounded chunk.
		// A failed check leaves an incomplete response with the original length.
		_, _ = io.CopyBuffer(attachmentGrantWriter{allow: allowed, out: w}, io.LimitReader(file, record.Bytes), make([]byte, 1<<20))
		return
	}
	http.NotFound(w, r)
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
