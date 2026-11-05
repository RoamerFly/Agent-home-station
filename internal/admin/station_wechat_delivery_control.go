package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/workspacefs"
)

type stationDeliveryStop struct {
	Account string    `json:"account"`
	Thread  string    `json:"thread"`
	Trace   string    `json:"trace"`
	Key     string    `json:"key"`
	At      time.Time `json:"at"`
}

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
	root, err := workspacefs.OpenPath(w.dir)
	if err != nil {
		return nil, "", apphost.ErrUnavailable
	}
	defer root.Close()
	f, err := root.Open("queue/messages-v2.json")
	if err != nil {
		return w, "", err
	}
	defer f.Close()
	var identity struct {
		Version int
		Account string
	}
	if json.NewDecoder(io.LimitReader(f, bot.MaxDeliveryCheckpointBytes+1)).Decode(&identity) != nil || !bot.SupportedDeliveryCheckpointVersion(identity.Version) || identity.Account == "" || wechatapp.ConnectionID(identity.Account) != id {
		return nil, "", apphost.ErrUnavailable
	}
	return w, identity.Account, nil
}

func deliveryStopID(in companion.Input) string {
	return localCheckpointKey(in.Account, in.Thread, in.TraceID)
}

// deliveryMu serializes the journal commit with entry to a native submission.
// It is separate from worker.mu: shutdown waits for the actor under worker.mu.
func (w *stationWechatWorker) readDeliveryStops() (map[string]stationDeliveryStop, error) {
	rows := map[string]stationDeliveryStop{}
	root, err := workspacefs.OpenPath(w.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat("delivery-stops.json")
	if errors.Is(err, os.ErrNotExist) {
		return rows, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return nil, errors.New("delivery cancellation journal unavailable")
	}
	f, err := root.Open("delivery-stops.json")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 || json.Unmarshal(raw, &rows) != nil || rows == nil || len(rows) > 5000 {
		return nil, errors.New("invalid delivery cancellation journal")
	}
	for key, row := range rows {
		if row.Account == "" || row.Thread == "" || row.Trace == "" || row.Key == "" || row.At.IsZero() || key != deliveryStopID(companion.Input{Account: row.Account, Thread: row.Thread, TraceID: row.Trace}) {
			return nil, errors.New("invalid delivery cancellation identity")
		}
	}
	return rows, nil
}

func (w *stationWechatWorker) deliveryCancelled(in companion.Input) (bool, error) {
	w.deliveryMu.Lock()
	defer w.deliveryMu.Unlock()
	rows, err := w.readDeliveryStops()
	_, cancelled := rows[deliveryStopID(in)]
	return cancelled, err
}

func (w *stationWechatWorker) withDeliverySubmit(ctx context.Context, in companion.Input, submit func() error) error {
	w.deliveryMu.Lock()
	defer w.deliveryMu.Unlock()
	rows, err := w.readDeliveryStops()
	if err != nil {
		return err
	}
	if _, cancelled := rows[deliveryStopID(in)]; cancelled {
		return bot.ErrDeliveryCancelled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return submit()
}

func (w *stationWechatWorker) saveDeliveryStop(stop stationDeliveryStop) error {
	rows, err := w.readDeliveryStops()
	if err != nil {
		return err
	}
	id := deliveryStopID(companion.Input{Account: stop.Account, Thread: stop.Thread, TraceID: stop.Trace})
	if _, exists := rows[id]; exists {
		return nil
	}
	if len(rows) >= 5000 {
		return errors.New("delivery cancellation journal full")
	}
	rows[id] = stop
	raw, err := json.Marshal(rows)
	if err != nil || len(raw) > 2<<20 {
		return errors.New("delivery cancellation journal full")
	}
	root, err := workspacefs.OpenPath(w.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	stage := ".stop-" + randomToken()
	defer root.Remove(stage)
	if err := writeLegacyStagedFile(root, stage, rows); err != nil {
		return err
	}
	return root.Rename(stage, "delivery-stops.json")
}

func (s *stationWechat) deliveryControlRoutes(owner *Server, mux *http.ServeMux) {
	mux.HandleFunc("POST /api/apps/wechat/connections/{connection}/receipts/{key}/cancel", owner.protected(s.cancelDelivery))
}

func (s *stationWechat) cancelDelivery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, key := r.PathValue("connection"), r.PathValue("key")
	if !validID(key) {
		fail(w, 400, "投递编号无效")
		return
	}
	worker, account, err := s.deliveryCheckpoint(id)
	if err != nil || worker == nil {
		fail(w, 404, "投递记录不存在")
		return
	}
	worker.deliveryMu.Lock()
	rows, err := bot.ReadDeliveryEvidencePage(filepath.Join(worker.dir, "queue"), account, "", 5000)
	row, found := rows.Records[key]
	if err != nil || !found || row.Trace == "" {
		worker.deliveryMu.Unlock()
		fail(w, 404, "投递记录不存在或无法核对原请求")
		return
	}
	stop := stationDeliveryStop{Account: account, Thread: row.Status.ThreadID, Trace: row.Trace, Key: key, At: time.Now().UTC()}
	err = worker.saveDeliveryStop(stop)
	worker.deliveryMu.Unlock()
	if err != nil {
		fail(w, 503, "停止意图尚未保存，请重试")
		return
	}
	// Stop the actual native segment queue promptly, even while the actor is
	// waiting for a model/tool. The actor also consumes the durable fence later.
	cancelCtx, stopCancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer stopCancel()
	native, nativeErr := s.WorkerSender(cancelCtx, id)
	for itemKey, item := range rows.Records {
		if nativeErr != nil || native.Account() != account {
			break
		}
		if cancelCtx.Err() != nil {
			break
		}
		if item.Trace != stop.Trace || item.Status.ThreadID != stop.Thread || item.Status.Kind == "text" || item.Status.State == "accepted" {
			continue
		}
		s.cancelNativeDelivery(cancelCtx, native, itemKey, item)
	}
	respond(w, http.StatusAccepted, map[string]any{"idempotency_key": key, "state": "cancellation_requested", "cancel_requested": true, "scope": "originating_turn"})
}

func (s *stationWechat) cancelNativeDelivery(ctx context.Context, native *linuxnative.Sender, key string, row bot.DeliveryEvidence) {
	if row.Status.State == "prepared" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var status linuxnative.ToolStatus
	var err error
	if row.Status.ID != "" {
		status, err = native.ToolSendStatus(ctx, row.Status.ID)
	} else {
		status, err = native.LookupSend(ctx, key)
	}
	if err != nil || status.ID == "" || row.Status.ID != "" && status.ID != row.Status.ID || status.ThreadID != row.Status.ThreadID || status.State == "accepted" {
		return
	}
	if row.ResumeOriginID != "" && status.ID == row.ResumeOriginID {
		return
	}
	if row.StrictSource && (status.SourceID == "" || status.SourceID != row.SourceID || status.AccountID != native.Account() || status.Kind != "send" && status.Kind != row.Status.Kind) {
		return
	}
	_, _ = native.CancelMediaJob(ctx, status.ID)
}
