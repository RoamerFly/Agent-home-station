package station

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
)

type stationRecallSource struct {
	worker                                 *stationWechatWorker
	account, thread, deliveryKey, serverID string
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
	if !stationRecallAccepted(ref, server) {
		receipt, err := s.ReadReceipt(ctx, id, key)
		if err != nil {
			return out, err
		}
		if receipt.ConnectionRef != s.connectionRef(id, account) || receipt.ConversationID != row.Status.ThreadID ||
			!stationRecallAccepted(receipt.DeliveryReceipt, server) {
			return out, apphost.ErrNotFound
		}
	}
	return stationRecallSource{w, account, row.Status.ThreadID, key, server}, nil
}

func stationRecallAccepted(receipt apphost.DeliveryReceipt, server string) bool {
	if receipt.State == "accepted" && receipt.ServerID == server {
		return true
	}
	for _, part := range receipt.AcceptedParts {
		if part.ServerID == server {
			return true
		}
	}
	return false
}

func (source stationRecallSource) operationID(id string) string {
	return "station-recall-" + localCheckpointKey(id, source.thread, source.serverID)
}

func (s *stationWechat) stationRecallNative(ctx context.Context, id, account string) (*linuxnative.Sender, error) {
	current, err := s.signedInAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	if current != account {
		return nil, apphost.ErrLifecycleConflict
	}
	native, err := s.WorkerSender(ctx, id)
	if err != nil {
		return nil, err
	}
	if native.Account() != account {
		return nil, apphost.ErrUnavailable
	}
	return native, nil
}

func (s *stationWechat) readStationRecall(ctx context.Context, id string, source stationRecallSource, cursor string) (wechatapp.RecallView, error) {
	var view wechatapp.RecallView
	ledger, paused, err := s.stationActionLedger(id)
	if err != nil {
		return view, err
	}
	if p, ok := ledger.FindConnected(s.installation, id, source.operationID(id), "", "recall"); ok {
		result := s.lookupStationAction(ctx, ledger, p)
		view.Operation = &result
		return view, nil
	}
	if paused {
		return view, apphost.ErrLifecycleConflict
	}
	scope := []byte(localCheckpointKey("recall", id, source.deliveryKey, source.thread, source.serverID))
	platformCursor := ""
	if cursor != "" {
		if len(cursor) > stationContactCursorLimit {
			return view, wechatapp.ErrRecallCursor
		}
		packet, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return view, wechatapp.ErrRecallCursor
		}
		plain, err := s.contactCursor.Open(nil, nil, packet, scope)
		if err != nil || len(plain) == 0 || len(plain) > 8192 {
			return view, wechatapp.ErrRecallCursor
		}
		platformCursor = string(plain)
	}
	native, err := s.stationRecallNative(ctx, id, source.account)
	if err != nil {
		return view, err
	}
	page, err := native.CatalogWithLimit(ctx, "messages", source.thread, "", platformCursor, 50)
	if err != nil {
		var catalog *linuxnative.CatalogError
		if errors.As(err, &catalog) && catalog.Code == "catalog_cursor_invalid" {
			return view, wechatapp.ErrRecallCursor
		}
		return view, err
	}
	for _, item := range page.Items {
		message, _ := item["id"].(string)
		if item["serverId"] != source.serverID || item["threadId"] != source.thread ||
			item["direction"] != "outgoing" || !wechatapp.ValidRecallMessageID(message) {
			continue
		}
		if view.MessageID != "" && view.MessageID != message {
			return wechatapp.RecallView{}, apphost.ErrLifecycleConflict
		}
		view.MessageID = message
	}
	if view.MessageID == "" && page.NextCursor != nil && *page.NextCursor != "" {
		if len(*page.NextCursor) > 8192 || *page.NextCursor == platformCursor {
			return view, wechatapp.ErrRecallCursor
		}
		view.NextCursor = base64.RawURLEncoding.EncodeToString(s.contactCursor.Seal(nil, nil, []byte(*page.NextCursor), scope))
	}
	return view, nil
}

func (s *stationWechat) submitStationRecall(ctx context.Context, id string, source stationRecallSource, messageID, owner string, active func() bool) (apphost.ActionReceipt, error) {
	var out apphost.ActionReceipt
	operationID := source.operationID(id)
	w := source.worker
	w.mu.Lock()
	ledger, err := w.actionLedgerLocked()
	w.mu.Unlock()
	if err != nil {
		return out, err
	}
	prior := func() (apphost.ActionReceipt, bool, error) {
		if p, ok := ledger.FindConnected(s.installation, id, operationID, "", "recall"); ok {
			if p.Request.MessageID != messageID || p.Request.ServerID != source.serverID || p.Request.ThreadID != source.thread {
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
	native, err := s.stationRecallNative(ctx, id, source.account)
	if err != nil {
		return out, err
	}
	message, err := native.Message(ctx, messageID)
	if err != nil || !wechatapp.OwnMessage(message, source.account, source.thread, messageID) || message.ServerID != source.serverID {
		return out, apphost.ErrLifecycleConflict
	}
	current, err := s.signedInAccount(ctx, id)
	if err != nil || current != source.account {
		return out, apphost.ErrLifecycleConflict
	}
	request := linuxnative.AdminAction{Kind: "recall", ThreadID: source.thread, MessageID: messageID, ServerID: source.serverID}
	intent := apphost.ActionIntent{ConnectionRef: s.connectionRef(id, source.account), Action: request.Kind,
		RequesterID: owner, Source: "local_admin", SourceThread: source.thread, SourceRequestID: source.deliveryKey,
		Target: source.thread, Purpose: "owner_approved_outgoing_recall", GrantVersion: "owner_approval:" + operationID, IdempotencyKey: operationID}
	p := wechatapp.ActionProposal{ID: operationID, Account: source.account, Request: request, State: "unknown", Created: time.Now().UTC(), AppAction: &intent}
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
