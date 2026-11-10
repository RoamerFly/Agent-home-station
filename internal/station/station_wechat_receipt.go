package station

import (
	"context"
	"errors"
	"os"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
)

func (s *stationWechat) ListReceipts(ctx context.Context, id, after string, limit int) (apphost.ReceiptPage, error) {
	page := apphost.ReceiptPage{Items: []apphost.Receipt{}, Coverage: "current_worker_checkpoint"}
	if !artifactID(id, "wechat-") || after != "" && !identity.ValidPathID(after) || limit < 1 {
		return page, apphost.ErrNotFound
	}
	w, account, err := s.deliveryCheckpoint(id)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, apphost.ErrNotFound) {
		return page, nil
	}
	if err != nil {
		return page, err
	}
	if w == nil {
		return page, nil
	}
	rows, err := bot.ReadDeliveryEvidencePage(w.queueDir(), account, after, limit)
	if err != nil {
		return page, apphost.ErrUnavailable
	}
	page.Snapshot, page.NextAfter = rows.Snapshot, rows.NextAfter
	w.deliveryMu.Lock()
	stops, err := w.readDeliveryStops()
	controls, controlErr := w.readResumeControls()
	w.deliveryMu.Unlock()
	if err != nil || controlErr != nil {
		return page, apphost.ErrUnavailable
	}
	page.Snapshot = localCheckpointKey(rows.Snapshot, stops, controls)
	for _, key := range rows.Keys {
		row := rows.Records[key]
		receipt := wechatapp.StoredWorkerReceipt(s.connectionRef(id, account), key, row)
		if control, exists := controls[key]; exists {
			receipt.Continuation = resumeControlView(control)
		}
		_, cancelled := stops[deliveryStopID(companion.Input{Account: account, Thread: row.Status.ThreadID, TraceID: row.Trace})]
		receipt.CancelRequested = receipt.CancelRequested || cancelled
		page.Items = append(page.Items, receipt)
	}
	return page, nil
}

func (s *stationWechat) ReadReceipt(ctx context.Context, id, key string) (apphost.Receipt, error) {
	if !identity.ValidPathID(key) {
		return apphost.Receipt{}, apphost.ErrNotFound
	}
	native, nativeErr := s.WorkerSender(ctx, id)
	if nativeErr != nil && (!errors.Is(nativeErr, apphost.ErrUnavailable) || ctx.Err() != nil) {
		return apphost.Receipt{}, nativeErr
	}
	var w *stationWechatWorker
	var account string
	var err error
	if native != nil {
		account = native.Account()
		w, err = s.worker(id, false)
	} else {
		// The owner can read host-held evidence during a native outage. A
		// missing queue is different from an invalid or foreign checkpoint.
		w, account, err = s.deliveryCheckpoint(id)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, apphost.ErrNotFound) {
			return s.readLegacyReceipt(ctx, id, key, nil)
		}
	}
	if err != nil {
		return apphost.Receipt{}, err
	}
	if w == nil {
		return apphost.Receipt{}, apphost.ErrNotFound
	}
	evidence, err := bot.ReadDeliveryEvidence(w.queueDir(), account, []string{key})
	if err != nil {
		return apphost.Receipt{}, apphost.ErrUnavailable
	}
	row, found := evidence[key]
	if !found {
		return s.readLegacyReceipt(ctx, id, key, native)
	}
	result := wechatapp.StoredWorkerReceipt(s.connectionRef(id, account), key, row)
	wechatapp.LookupWorkerReceipt(ctx, native, row, &result.DeliveryReceipt)
	cancelled, err := w.deliveryCancelled(companion.Input{Account: account, Thread: row.Status.ThreadID, TraceID: row.Trace})
	if err != nil {
		return apphost.Receipt{}, apphost.ErrUnavailable
	}
	result.CancelRequested = result.CancelRequested || cancelled
	w.deliveryMu.Lock()
	controls, err := w.readResumeControls()
	w.deliveryMu.Unlock()
	if err != nil {
		return apphost.Receipt{}, apphost.ErrUnavailable
	}
	if control, exists := controls[key]; exists {
		result.Continuation = resumeControlView(control)
	}
	return result, nil
}
