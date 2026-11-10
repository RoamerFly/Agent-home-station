package station

import (
	"context"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
)

// rollbackActionEvidence snapshots every connected group mutation and recall
// reservation for one connection.  It only reads the ledger and, when a
// native sender is provided, queries the original operation or idempotency
// key.  It never records refreshed results, retries, or submits an action.
//
// The returned digest is the durable-ledger snapshot before native lookups.
// Thus callers can read it again with native=nil after a drain and prove that
// no local action reservation changed while the platform checks ran.
func (s *stationWechat) rollbackActionEvidence(ctx context.Context, w *stationWechatWorker, ref apphost.ConnectionRef, native *linuxnative.Sender) ([]apphost.ActionReceipt, string, error) {
	if s == nil || w == nil || ref.AppID != wechatapp.ID || ref.ConnectionID != wechatapp.ConnectionID(ref.AccountID) {
		return nil, "", apphost.ErrNotFound
	}
	w.mu.Lock()
	ledger, err := w.actionLedgerLocked()
	if err != nil {
		w.mu.Unlock()
		return nil, "", err
	}
	proposals, snapshot, err := ledger.SnapshotConnected(ref)
	w.mu.Unlock()
	if err != nil {
		return nil, "", err
	}
	return wechatapp.ProjectRollbackActions(ctx, native, proposals), snapshot, nil
}
