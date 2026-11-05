package admin

import (
	"context"
	"path/filepath"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
)

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
	queryWechatDelivery(ctx, native, row.Status.ThreadID, row.SourceID, row.Status, false, &result.DeliveryReceipt)
	return result, nil
}
