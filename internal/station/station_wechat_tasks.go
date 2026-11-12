package station

import (
	"net/http"
	"strconv"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/websession"
)

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
