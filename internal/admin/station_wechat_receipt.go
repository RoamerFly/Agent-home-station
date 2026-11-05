package admin

import (
	"context"
	"errors"
	"path/filepath"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/linuxnative"
)

func (s *stationWechat) ListReceipts(ctx context.Context, id, after string, limit int) (apphost.ReceiptPage, error) {
	page := apphost.ReceiptPage{Items: []apphost.Receipt{}, Coverage: "current_worker_checkpoint"}
	if after != "" && !validID(after) || limit < 1 {
		return page, apphost.ErrNotFound
	}
	native, err := s.WorkerSender(ctx, id)
	if err != nil {
		return page, err
	}
	w, err := s.worker(id, false)
	if err != nil {
		return page, err
	}
	if w == nil {
		return page, nil
	}
	rows, err := bot.ReadDeliveryEvidencePage(filepath.Join(w.dir, "queue"), native.Account(), after, limit)
	if err != nil {
		return page, apphost.ErrUnavailable
	}
	page.Snapshot, page.NextAfter = rows.Snapshot, rows.NextAfter
	for _, key := range rows.Keys {
		row := rows.Records[key]
		receipt := apphost.Receipt{ConnectionRef: s.connectionRef(id, native.Account()), ConversationID: row.Status.ThreadID,
			DeliveryReceipt: apphost.DeliveryReceipt{IdempotencyKey: key, Kind: row.Status.Kind, Lookup: "stored"}}
		projectWechatReceipt(&receipt.DeliveryReceipt, row.Status)
		page.Items = append(page.Items, receipt)
	}
	return page, nil
}

func (s *stationWechat) ReadReceipt(ctx context.Context, id, key string) (apphost.Receipt, error) {
	if !validID(key) {
		return apphost.Receipt{}, apphost.ErrNotFound
	}
	native, err := s.WorkerSender(ctx, id)
	if err != nil {
		return apphost.Receipt{}, err
	}
	w, err := s.worker(id, false)
	if err != nil {
		return apphost.Receipt{}, err
	}
	if w == nil {
		return apphost.Receipt{}, apphost.ErrNotFound
	}
	evidence, err := bot.ReadDeliveryEvidence(filepath.Join(w.dir, "queue"), native.Account(), []string{key})
	if err != nil {
		return apphost.Receipt{}, apphost.ErrUnavailable
	}
	row, found := evidence[key]
	if !found {
		return s.readLegacyReceipt(ctx, id, key, native, w.dir)
	}
	result := apphost.Receipt{
		ConnectionRef:   s.connectionRef(id, native.Account()),
		ConversationID:  row.Status.ThreadID,
		DeliveryReceipt: apphost.DeliveryReceipt{IdempotencyKey: key, Kind: row.Status.Kind, Lookup: "stored"},
	}
	var reader any = native
	if row.StrictSource {
		reader = stationMediaReceiptReader{native, row.SourceID, row.Status.Kind}
	}
	queryWechatDelivery(ctx, reader, row.Status.ThreadID, row.SourceID, row.Status, row.StrictSource && row.Status.ID != "", &result.DeliveryReceipt)
	return result, nil
}

type stationMediaReceiptReader struct {
	native       *linuxnative.Sender
	source, kind string
}

func (r stationMediaReceiptReader) validate(status linuxnative.ToolStatus, err error) (linuxnative.ToolStatus, error) {
	if err == nil && (r.source == "" || status.SourceID != r.source || status.AccountID != r.native.Account() || status.Kind != "send" && status.Kind != r.kind) {
		return linuxnative.ToolStatus{}, errors.New("station_media_receipt_identity_mismatch")
	}
	return status, err
}

func (r stationMediaReceiptReader) LookupSend(ctx context.Context, key string) (linuxnative.ToolStatus, error) {
	return r.validate(r.native.LookupSend(ctx, key))
}

func (r stationMediaReceiptReader) ToolSendStatus(ctx context.Context, id string) (linuxnative.ToolStatus, error) {
	return r.validate(r.native.ToolSendStatus(ctx, id))
}
