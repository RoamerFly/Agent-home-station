package station

import (
	"context"
	"errors"
	"os"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
)

type stationRecallSource struct {
	worker *stationWechatWorker
	wechatapp.StationRecallTarget
}

func (s *stationWechat) stationRecallEvidence(ctx context.Context, id, key, server string) (stationRecallSource, error) {
	var out stationRecallSource
	if !identity.ValidPathID(key) || !wechatapp.ValidServerReceiptID(server) {
		return out, apphost.ErrNotFound
	}
	w, account, err := s.deliveryCheckpoint(id)
	if errors.Is(err, os.ErrNotExist) {
		return out, apphost.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	row, ok, err := wechatapp.ReadWorkerDelivery(w.runtimeStateDir(), account, key)
	if err != nil {
		return out, apphost.ErrUnavailable
	}
	if !ok || !identity.ValidPathID(row.Status.ThreadID) {
		return out, apphost.ErrNotFound
	}
	ref := apphost.DeliveryReceipt{IdempotencyKey: key, Kind: row.Status.Kind}
	wechatapp.ProjectDeliveryReceipt(&ref, row.Status)
	if !wechatapp.AcceptedRecallReceipt(ref, server) {
		receipt, err := s.ReadReceipt(ctx, id, key)
		if err != nil {
			return out, err
		}
		if receipt.ConnectionRef != s.connectionRef(id, account) || receipt.ConversationID != row.Status.ThreadID ||
			!wechatapp.AcceptedRecallReceipt(receipt.DeliveryReceipt, server) {
			return out, apphost.ErrNotFound
		}
	}
	return stationRecallSource{worker: w, StationRecallTarget: wechatapp.StationRecallTarget{
		Account: account, Thread: row.Status.ThreadID, DeliveryKey: key, ServerID: server,
	}}, nil
}

func (s *stationWechat) readStationRecall(ctx context.Context, id string, source stationRecallSource, cursor string) (wechatapp.RecallView, error) {
	var view wechatapp.RecallView
	ledger, paused, err := s.stationActionLedger(id)
	if err != nil {
		return view, err
	}
	if p, ok := ledger.FindConnected(s.installation, id, source.OperationID(id), "", "recall"); ok {
		result := s.lookupStationAction(ctx, ledger, p)
		view.Operation = &result
		return view, nil
	}
	if paused {
		return view, apphost.ErrLifecycleConflict
	}
	return s.directoryAccess().ReadRecall(ctx, id, source.StationRecallTarget, cursor)
}

func (s *stationWechat) submitStationRecall(ctx context.Context, id string, source stationRecallSource, messageID, owner string, active func() bool) (apphost.ActionReceipt, error) {
	var out apphost.ActionReceipt
	operationID := source.OperationID(id)
	w := source.worker
	w.mu.Lock()
	ledger, err := w.actionLedgerLocked()
	w.mu.Unlock()
	if err != nil {
		return out, err
	}
	prior := func() (apphost.ActionReceipt, bool, error) {
		if p, ok := ledger.FindConnected(s.installation, id, operationID, "", "recall"); ok {
			if p.Request.MessageID != messageID || p.Request.ServerID != source.ServerID || p.Request.ThreadID != source.Thread {
				return out, true, apphost.ErrLifecycleConflict
			}
			return p.Receipt(), true, nil
		}
		return out, false, nil
	}
	if !active() {
		return out, apphost.ErrLifecycleConflict
	}
	if result, found, err := prior(); found {
		return result, err
	}
	native, request, err := s.directoryAccess().PrepareRecall(ctx, id, source.StationRecallTarget, messageID)
	if err != nil {
		return out, err
	}
	intent := apphost.ActionIntent{ConnectionRef: s.connectionRef(id, source.Account), Action: request.Kind,
		RequesterID: owner, Source: "local_admin", SourceThread: source.Thread, SourceRequestID: source.DeliveryKey,
		Target: source.Thread, Purpose: "owner_approved_outgoing_recall", GrantVersion: "owner_approval:" + operationID, IdempotencyKey: operationID}
	p := wechatapp.ActionProposal{ID: operationID, Account: source.Account, Request: request, State: "unknown", Created: time.Now().UTC(), AppAction: &intent}
	w.mu.Lock()
	if ctx.Err() != nil || !active() {
		w.mu.Unlock()
		return out, apphost.ErrLifecycleConflict
	}
	if result, found, err := prior(); found {
		w.mu.Unlock()
		return result, err
	}
	if w.policy.Paused {
		w.mu.Unlock()
		return out, apphost.ErrLifecycleConflict
	}
	if err := s.admitPlatformWrite(ctx, id, native); err != nil {
		w.mu.Unlock()
		return out, err
	}
	submit, err := ledger.ReserveConnected(p, native)
	w.mu.Unlock()
	if err != nil {
		return out, err
	}
	return submit(ctx)
}
