package station

import (
	"net/http"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

func (w *stationWechatWorker) nextMediaResume(now time.Time) (*bot.MediaResumeRequest, error) {
	w.deliveryMu.Lock()
	defer w.deliveryMu.Unlock()
	return w.deliveryJournal().NextResume(now)
}

func (w *stationWechatWorker) mediaResumeResult(request bot.MediaResumeRequest, result bot.MediaResumeResult) error {
	w.deliveryMu.Lock()
	defer w.deliveryMu.Unlock()
	return w.deliveryJournal().RecordResumeResult(request, result)
}

func (s *stationWechat) requestDeliveryResume(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var input struct {
		RequestKey string `json:"request_key"`
	}
	if !websession.Decode(w, r, &input) {
		return
	}
	key := r.PathValue("key")
	if !identity.ValidPathID(input.RequestKey) || !identity.ValidPathID(key) {
		websession.Fail(w, 400, "请提供有效的原投递编号和请求编号")
		return
	}
	id := r.PathValue("connection")
	worker, account, err := s.deliveryCheckpoint(id)
	if err != nil || worker == nil {
		websession.Fail(w, 404, "原投递记录不存在")
		return
	}
	worker.deliveryMu.Lock()
	defer worker.deliveryMu.Unlock()
	rows, err := worker.deliveryJournal().ReadResumes()
	if err != nil {
		websession.Fail(w, 503, "续办请求暂不可读")
		return
	}
	if saved, exists := rows[key]; exists {
		websession.Respond(w, 202, map[string]any{"idempotency_key": key, "continuation": wechatapp.ResumeControlView(saved)})
		return
	}
	fence := s.platformFence(id)
	fence.RLock()
	defer fence.RUnlock()
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		websession.Fail(w, 503, "连接状态暂不可读")
		return
	}
	policy, err := readStationWechatPolicy(root)
	root.Close()
	if err != nil {
		websession.Fail(w, 503, "连接状态暂不可读")
		return
	}
	if !policy.RollbackFrozenAt.IsZero() {
		websession.Fail(w, 409, "连接已为回退停发，未登记新续发")
		return
	}
	old, exists, err := wechatapp.ReadWorkerDelivery(worker.runtimeStateDir(), account, key)
	if err != nil || !exists || old.Trace == "" {
		websession.Fail(w, 404, "原投递记录不存在")
		return
	}
	stops, err := worker.deliveryJournal().ReadStops()
	if err != nil {
		websession.Fail(w, 503, "停止记录暂不可读")
		return
	}
	_, stopped := stops[wechatapp.DeliveryStopID(companion.Input{Account: account, Thread: old.Status.ThreadID, TraceID: old.Trace})]
	if old.Status.Kind != "voice" || old.Status.State == "accepted" || !stopped && !old.Status.CancelRequested && old.Status.State != "cancelled" {
		websession.Fail(w, 409, "请选择已停止的分段语音；已完整送达的内容无需续发")
		return
	}
	if old.Status.ID == "" && old.Status.State == "cancelled" {
		websession.Fail(w, 409, "原语音尚未提交分段任务，可重新请求发送原成品")
		return
	}
	// Keep the original blocked forever; only this new explicit operation may run.
	if err := worker.deliveryJournal().SaveStop(wechatapp.DeliveryStop{Account: account, Thread: old.Status.ThreadID, Trace: old.Trace, Key: key, At: time.Now().UTC()}); err != nil {
		websession.Fail(w, 503, "原任务停止状态尚未保存")
		return
	}
	saved, err := worker.deliveryJournal().RequestResume(account, key, input.RequestKey, old.Status.ThreadID, old.Trace, rows, time.Now().UTC())
	if err != nil {
		websession.Fail(w, 503, "续办请求尚未保存，请重试")
		return
	}
	websession.Respond(w, 202, map[string]any{"idempotency_key": key, "continuation": wechatapp.ResumeControlView(saved)})
}
