package admin

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strconv"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func wechatConnectionRef(account string) apphost.ConnectionRef {
	return apphost.ConnectionRef{AppID: wechatAppID, InstallationID: wechatInstallationID,
		ConnectionID: wechatConnectionID(account), AccountID: account}
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
	allowed, err := m.authorizeConversation(ctx, thread)
	if err != nil {
		return apphost.Event{}, err
	}
	if !allowed {
		return apphost.Event{}, apphost.ErrNotFound
	}
	scope := companion.ScopeID("wechat", item.record.ID, thread)
	ev, err := readWechatSource(ctx, m.memory, scope, item.record.ID, thread, message)
	if err != nil {
		return apphost.Event{}, err
	}
	src := ev.Source
	out := apphost.Event{
		ConnectionRef: wechatConnectionRef(item.record.ID), EventRef: appEventRef(ev),
		ConversationID: thread, DatasetID: src.InstanceID, MessageType: src.MessageType, OccurredAt: src.OccurredAt,
		ReceivedAt: src.ReceivedAt, StoredSequence: ev.Seq, SourceSequence: src.Sequence,
		AddresseeIDs: ev.Addressees, ReadScope: "source_metadata",
	}
	if q := ev.QuotedSource; q != nil && q.Account == src.Account && q.Thread == src.Thread && q.InstanceID == src.InstanceID {
		quoted, err := readWechatSource(ctx, m.memory, scope, src.Account, thread, q.MessageID)
		if err != nil && !errors.Is(err, apphost.ErrNotFound) {
			return apphost.Event{}, err
		}
		if err == nil && q.ServerID != "" && q.ServerID == quoted.Source.ServerID && q.Sender == quoted.Sender &&
			q.Revision == quoted.Source.Revision && (q.EventID == "" || q.EventID == quoted.ID) {
			ref := appEventRef(quoted)
			out.Quote = &ref
		}
	}
	for _, media := range ev.IncomingMedia {
		if media.Source.EventID == ev.ID && media.Source.Revision == src.Revision {
			out.Media = append(out.Media, apphost.MediaRef{AssetID: media.AssetID, Kind: media.Kind})
		}
	}
	return out, nil
}

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

func appEventRef(ev companion.MemoryEvent) apphost.EventRef {
	return apphost.EventRef{EventID: ev.ID, MessageID: ev.Source.MessageID, ServerID: ev.Source.ServerID,
		SenderID: ev.Sender, Revision: ev.Source.Revision}
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
