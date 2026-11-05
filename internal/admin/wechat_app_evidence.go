package admin

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strconv"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func wechatConnectionRef(account string) apphost.ConnectionRef {
	return wechatapp.ConnectionRef(account)
}

func (w wechatCompatibility) ReadEvent(ctx context.Context, connection, thread, message string) (apphost.Event, error) {
	item, release, err := w.acquire(connection)
	if err != nil {
		return apphost.Event{}, err
	}
	defer release()
	if !validID(thread) || message == "" || len(message) > 8192 {
		return apphost.Event{}, apphost.ErrNotFound
	}
	m := item.manager
	if m == nil || m.memory == nil || m.native.Account() != item.record.ID {
		return apphost.Event{}, apphost.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return wechatapp.ReadEvent(ctx, item.record.ID, thread, message, m.authorizeConversation,
		func(ctx context.Context, sourceThread, sourceMessage string) (wechatapp.SourceRecord, error) {
			if sourceThread != thread {
				return wechatapp.SourceRecord{}, apphost.ErrNotFound
			}
			scope := companion.ScopeID(wechatapp.ID, item.record.ID, thread)
			ev, err := readWechatSource(ctx, m.memory, scope, item.record.ID, thread, sourceMessage)
			if err != nil {
				return wechatapp.SourceRecord{}, err
			}
			record := wechatapp.SourceRecord{Source: *wechatSourceMetadata(ev.Source), StoredSequence: ev.Seq, AddresseeIDs: ev.Addressees, QuotedSource: wechatSourceMetadata(ev.QuotedSource)}
			for _, media := range ev.IncomingMedia {
				record.Media = append(record.Media, wechatapp.SourceMedia{EventID: media.Source.EventID, Revision: media.Source.Revision, AssetID: media.AssetID, Kind: media.Kind})
			}
			return record, nil
		})
}

func wechatSourceMetadata(src *companion.SourceRef) *wechatapp.SourceMetadata {
	if src == nil {
		return nil
	}
	return &wechatapp.SourceMetadata{EventID: src.EventID, InstanceID: src.InstanceID, Account: src.Account,
		Thread: src.Thread, MessageID: src.MessageID, ServerID: src.ServerID, Sender: src.Sender,
		OccurredAt: src.OccurredAt, ReceivedAt: src.ReceivedAt, Revision: src.Revision,
		MessageType: src.MessageType, Sequence: src.Sequence}
}

// Storage, forgetting and source-version checks remain host authority. The
// application receives metadata from a reader already bound to this account.
func readWechatSource(ctx context.Context, store *companion.Store, scope, account, thread, message string) (companion.MemoryEvent, error) {
	id, err := store.OriginalEventID(ctx, scope, message)
	var ev companion.MemoryEvent
	if err == nil {
		ev, err = store.ReadEvent(ctx, []string{scope}, id)
	}
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, companion.ErrMemoryConflict) {
		return companion.MemoryEvent{}, apphost.ErrNotFound
	}
	if err != nil {
		return companion.MemoryEvent{}, err
	}
	src := ev.Source
	if src == nil || src.EventID != id || src.InstanceID != store.SourceInstanceID() || src.MessageID != message ||
		src.Account != account || src.Thread != thread || src.Sender != ev.Sender || src.Revision <= 0 {
		return companion.MemoryEvent{}, apphost.ErrNotFound
	}
	return ev, nil
}

type wechatReceiptReader interface {
	LookupSend(context.Context, string) (linuxnative.ToolStatus, error)
}

func (w wechatCompatibility) ReadReceipt(ctx context.Context, connection, key string) (apphost.Receipt, error) {
	item, release, err := w.acquire(connection)
	if err != nil {
		return apphost.Receipt{}, err
	}
	defer release()
	if !validID(key) {
		return apphost.Receipt{}, apphost.ErrNotFound
	}
	m := item.manager
	if m == nil || m.tools == nil || m.native.Account() != item.record.ID {
		return apphost.Receipt{}, apphost.ErrUnavailable
	}
	record, exists, err := m.tools.record(key, item.record.ID)
	if err != nil {
		return apphost.Receipt{}, err
	}
	if !exists {
		return m.readAgentDelivery(ctx, key)
	}
	result := apphost.Receipt{
		ConnectionRef: wechatConnectionRef(item.record.ID), ConversationID: record.Thread,
		DeliveryReceipt: apphost.DeliveryReceipt{IdempotencyKey: record.Key, Kind: record.Kind, Lookup: "stored"},
	}
	if record.archived {
		projectWechatReceipt(&result.DeliveryReceipt, record.Status)
	} else {
		m.queryWechatDelivery(ctx, record.Thread, record.Status.SourceID, record.Status, false, &result.DeliveryReceipt)
	}
	return result, nil
}

func (m *Manager) queryWechatDelivery(ctx context.Context, thread, source string, stored linuxnative.ToolStatus, byJob bool, result *apphost.DeliveryReceipt) {
	projectWechatReceipt(result, stored)
	if result.State == "accepted" || (result.State == "not_submitted" && stored.ID == "") {
		return
	}
	if err := ctx.Err(); err != nil {
		result.Lookup = "unavailable"
		return
	}
	var lookup func(context.Context, string) (linuxnative.ToolStatus, error)
	key := result.IdempotencyKey
	if byJob {
		key = stored.ID
		if reader, ok := m.native.(interface {
			ToolSendStatus(context.Context, string) (linuxnative.ToolStatus, error)
		}); ok {
			lookup = reader.ToolSendStatus
		}
	} else if reader, ok := m.native.(wechatReceiptReader); ok {
		lookup = reader.LookupSend
	}
	if key == "" {
		return
	}
	if lookup == nil {
		result.Lookup = "unavailable"
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	status, err := lookup(ctx, key)
	switch {
	case errors.Is(err, linuxnative.ErrToolNotFound):
		result.Lookup = "not_found"
	case err != nil:
		result.Lookup = "unavailable"
	case !validID(status.ID) || status.ThreadID != thread || (status.Kind != "" && status.Kind != "send" && status.Kind != result.Kind) ||
		(stored.ID != "" && status.ID != stored.ID) || (source != "" && status.SourceID != "" && status.SourceID != source):
		result.Lookup = "identity_mismatch"
	default:
		result.Lookup = "platform"
		projectWechatReceipt(result, status)
	}
}

var wechatResultCode = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,127}$`)

func projectWechatReceipt(out *apphost.DeliveryReceipt, status linuxnative.ToolStatus) {
	out.State, out.OperationID = "submission_unknown", status.ID
	out.CancelRequested = out.CancelRequested || status.CancelRequested
	out.PlatformState, out.PlatformCode, out.ServerID = "", "", ""
	if wechatResultCode.MatchString(status.State) {
		out.PlatformState = status.State
	}
	if wechatResultCode.MatchString(status.ErrorCode) {
		out.PlatformCode = status.ErrorCode
	} else if wechatResultCode.MatchString(status.Reason) {
		out.PlatformCode = status.Reason
	}
	accepted := make(map[int]string, len(out.AcceptedParts))
	for _, part := range out.AcceptedParts {
		accepted[part.Index] = part.ServerID
	}
	partsComplete := !status.VoicePartsIncomplete && (len(status.VoiceChunks) > 0 || len(accepted) == 0)
	out.UnconfirmedParts = nil
	out.PartsIncomplete = status.VoicePartsIncomplete
	seen := map[int]bool{}
	for _, chunk := range status.VoiceChunks {
		if seen[chunk.Index] {
			partsComplete = false
			continue
		}
		seen[chunk.Index] = true
		if chunk.State != "accepted" || !chunk.HasReceipt || !wechatServerReceiptID(chunk.ServerID) {
			partsComplete = false
			if accepted[chunk.Index] == "" {
				partState := "unknown"
				switch chunk.State {
				case "prepared", "submitting", "rejected", "needs_review", "cancelled":
					partState = chunk.State
				}
				out.UnconfirmedParts = append(out.UnconfirmedParts, apphost.ReceiptPart{Index: chunk.Index, State: partState})
			}
			continue
		}
		if previous, exists := accepted[chunk.Index]; exists {
			partsComplete = partsComplete && previous == chunk.ServerID
			continue
		}
		accepted[chunk.Index] = chunk.ServerID
		out.AcceptedParts = append(out.AcceptedParts, apphost.ReceiptPart{Index: chunk.Index, ServerID: chunk.ServerID})
	}
	switch status.State {
	case "accepted":
		if validID(status.ID) && wechatServerReceiptID(status.ServerID) && partsComplete {
			out.State, out.ServerID = "accepted", status.ServerID
		}
	case "prepared":
		out.State = "not_submitted"
	case "queued", "running", "sending", "submitting":
		out.State = "submitted_pending"
	case "failed", "rejected":
		out.State = "failed"
	case "cancelled":
		out.State = "cancelled"
	}
}

func wechatServerReceiptID(value string) bool {
	id, err := strconv.ParseUint(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatUint(id, 10) == value
}
