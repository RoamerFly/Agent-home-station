package adapter

import (
	"context"
	"errors"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
)

// StationRecallTarget must come from the host's accepted delivery evidence.
type StationRecallTarget struct {
	Account, Thread, DeliveryKey, ServerID string
}

func (target StationRecallTarget) OperationID(connection string) string {
	return "station-recall-" + stationDirectoryScope(connection, target.Thread, target.ServerID)
}

func AcceptedRecallReceipt(receipt apphost.DeliveryReceipt, server string) bool {
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

// ReadRecall projects only the exact outgoing candidate from a native page.
func (a StationDirectoryAccess) ReadRecall(ctx context.Context, connection string, target StationRecallTarget, cursor string) (RecallView, error) {
	if a.Cursor == nil {
		return RecallView{}, apphost.ErrUnavailable
	}
	scope := stationDirectoryScope("recall", connection, target.DeliveryKey, target.Thread, target.ServerID)
	current, err := openStationDirectoryCursor(a.Cursor, scope, "", cursor)
	if err != nil {
		return RecallView{}, ErrRecallCursor
	}
	native, err := a.accountSender(ctx, connection, target.Account)
	if err != nil {
		return RecallView{}, err
	}
	page, err := native.CatalogWithLimit(ctx, "messages", target.Thread, "", current, 50)
	if err != nil {
		var catalog *linuxnative.CatalogError
		if errors.As(err, &catalog) && catalog.Code == "catalog_cursor_invalid" {
			return RecallView{}, ErrRecallCursor
		}
		return RecallView{}, err
	}
	if account, err := a.SignedInAccount(ctx, connection); err != nil {
		return RecallView{}, err
	} else if account != target.Account {
		return RecallView{}, apphost.ErrLifecycleConflict
	}
	var out RecallView
	for _, item := range page.Items {
		message, _ := item["id"].(string)
		if item["serverId"] != target.ServerID || item["threadId"] != target.Thread ||
			item["direction"] != "outgoing" || !ValidRecallMessageID(message) {
			continue
		}
		if out.MessageID != "" && out.MessageID != message {
			return RecallView{}, apphost.ErrLifecycleConflict
		}
		out.MessageID = message
	}
	if out.MessageID == "" {
		out.NextCursor, err = nextStationDirectoryCursor(a.Cursor, scope, "", current, page.NextCursor)
		if err != nil {
			return RecallView{}, ErrRecallCursor
		}
	}
	return out, nil
}

// PrepareRecall re-reads the candidate before the host reserves a write.
func (a StationDirectoryAccess) PrepareRecall(ctx context.Context, connection string, target StationRecallTarget, messageID string) (*linuxnative.Sender, linuxnative.AdminAction, error) {
	var request linuxnative.AdminAction
	if !ValidRecallMessageID(messageID) {
		return nil, request, apphost.ErrLifecycleConflict
	}
	native, err := a.accountSender(ctx, connection, target.Account)
	if err != nil {
		return nil, request, err
	}
	message, err := native.Message(ctx, messageID)
	if err != nil || !OwnMessage(message, target.Account, target.Thread, messageID) || message.ServerID != target.ServerID {
		return nil, request, apphost.ErrLifecycleConflict
	}
	if current, err := a.SignedInAccount(ctx, connection); err != nil || current != target.Account {
		return nil, request, apphost.ErrLifecycleConflict
	}
	request = linuxnative.AdminAction{Kind: "recall", ThreadID: target.Thread, MessageID: messageID, ServerID: target.ServerID}
	return native, request, nil
}
