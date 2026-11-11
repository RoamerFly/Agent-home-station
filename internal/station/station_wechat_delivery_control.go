package station

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"

	"wechat-bot/internal/websession"
)

// The persisted worker identity is sufficient to stop local work while the
// native service is offline. It is never sufficient to authorize a native send.
func (s *stationWechat) deliveryCheckpoint(id string) (*stationWechatWorker, string, error) {
	if !artifactID(id, "wechat-") {
		return nil, "", apphost.ErrNotFound
	}
	w, err := s.worker(id, false)
	if err != nil {
		return nil, "", err
	}
	if w == nil {
		return nil, "", apphost.ErrNotFound
	}
	account, err := wechatapp.WorkerCheckpointAccount(w.runtimeStateDir(), id)
	if err != nil {
		return w, "", err
	}
	return w, account, nil
}

func (w *stationWechatWorker) deliveryJournal() wechatapp.DeliveryJournal {
	return wechatapp.DeliveryJournal(w.runtimeStateDir())
}

// deliveryMu serializes the journal commit with entry to a native submission.
// It is separate from worker.mu: shutdown waits for the actor under worker.mu.
func (w *stationWechatWorker) withDeliverySubmit(ctx context.Context, in companion.Input, submit func() error) error {
	w.deliveryMu.Lock()
	defer w.deliveryMu.Unlock()
	return w.deliveryJournal().WithSubmit(ctx, in, submit)
}

func (w *stationWechatWorker) deliveryCancelled(in companion.Input) (bool, error) {
	w.deliveryMu.Lock()
	defer w.deliveryMu.Unlock()
	rows, err := w.deliveryJournal().ReadStops()
	_, cancelled := rows[wechatapp.DeliveryStopID(in)]
	return cancelled, err
}

func (s *stationWechat) cancelDelivery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, key := r.PathValue("connection"), r.PathValue("key")
	if !identity.ValidPathID(key) {
		websession.Fail(w, 400, "投递编号无效")
		return
	}
	if strings.HasPrefix(key, "cross-reply-") {
		s.cancelCrossMemoryDelivery(w, r, id, key)
		return
	}
	worker, account, err := s.deliveryCheckpoint(id)
	if err != nil || worker == nil {
		websession.Fail(w, 404, "投递记录不存在")
		return
	}
	worker.deliveryMu.Lock()
	// Cancellation needs the complete bounded checkpoint, not a UI page.
	rows, err := wechatapp.ReadWorkerDeliveryPage(worker.runtimeStateDir(), account, "", math.MaxInt)
	row, found := rows.Records[key]
	if err != nil || !found || row.Trace == "" {
		worker.deliveryMu.Unlock()
		websession.Fail(w, 404, "投递记录不存在或无法核对原请求")
		return
	}
	stop := wechatapp.DeliveryStop{Account: account, Thread: row.Status.ThreadID, Trace: row.Trace, Key: key, At: time.Now().UTC()}
	err = worker.deliveryJournal().SaveStop(stop)
	traces := map[string]bool{stop.Trace: true}
	if err == nil {
		traces, err = worker.deliveryJournal().CancelResumes(stop)
	}
	worker.deliveryMu.Unlock()
	if err != nil {
		websession.Fail(w, 503, "停止意图尚未保存，请重试")
		return
	}
	// Stop the actual native segment queue promptly, even while the actor is
	// waiting for a model/tool. The actor also consumes the durable fence later.
	cancelCtx, stopCancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer stopCancel()
	native, nativeErr := s.WorkerSender(cancelCtx, id)
	if nativeErr == nil && native.Account() == account {
		wechatapp.CancelWorkerMedia(cancelCtx, native, key, row)
		for _, itemKey := range rows.Keys {
			if cancelCtx.Err() != nil {
				break
			}
			item := rows.Records[itemKey]
			if itemKey != key && traces[item.Trace] && item.Status.ThreadID == stop.Thread {
				wechatapp.CancelWorkerMedia(cancelCtx, native, itemKey, item)
			}
		}
	}
	websession.Respond(w, http.StatusAccepted, map[string]any{"idempotency_key": key, "state": "cancellation_requested", "cancel_requested": true, "scope": "originating_turn"})
}
