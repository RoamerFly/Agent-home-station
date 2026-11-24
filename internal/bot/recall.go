package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"wechat-bot/internal/linuxnative"
)

// recallIntent is persisted before the one native write. A lost POST response
// cannot be looked up by request key, so an intent without an operation ID is
// never submitted again after a restart.
type recallIntent struct {
	DeliveryKey string `json:"delivery_key"`
	Thread      string `json:"thread"`
	MessageID   string `json:"message_id"`
	ServerID    string `json:"server_id"`
	Key         string `json:"key"`
	OperationID string `json:"operation_id,omitempty"`
	State       string `json:"state"`
}

type nativeRecallSender interface {
	Account() string
	Catalog(context.Context, string, string, string, string) (linuxnative.CatalogPage, error)
	ExecuteAction(context.Context, linuxnative.AdminAction, string) (linuxnative.ActionStatus, error)
	InspectAction(context.Context, string) (linuxnative.ActionStatus, error)
}

var _ nativeRecallSender = (*linuxnative.Sender)(nil)

// recallAcceptedPlanBubble withdraws exactly one accepted text bubble selected
// by the model's read-only correction decision. The returned state is
// succeeded, failed, pending, unknown or unavailable. Only succeeded proves a
// remote recall; unknown is terminal for this intent because native recall has
// no lookup by idempotency key.
func (w *Worker) recallAcceptedPlanBubble(ctx context.Context, p *utterancePlan, sentIndex int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "unavailable", err
	}
	if w.runtime == nil || p == nil || sentIndex < 0 || sentIndex >= p.Index || sentIndex >= len(p.Sent) {
		return "unavailable", errors.New("recall plan index unavailable")
	}
	native, ok := w.sender.(nativeRecallSender)
	if !ok || native.Account() != w.source.Account() ||
		w.runtime.Account != "" && w.runtime.Account != native.Account() ||
		p.Input.Account != "" && p.Input.Account != native.Account() {
		return "unavailable", errors.New("native recall unavailable for this account")
	}
	d, err := w.acceptedBubbleDelivery(p, sentIndex)
	if err != nil {
		return "unavailable", err
	}
	for _, existing := range w.runtime.Recalls {
		if existing.DeliveryKey == d.Key {
			if existing.Thread != d.Thread || existing.ServerID != d.ServerID {
				return "unavailable", errors.New("recall intent changed identity")
			}
			return w.inspectRecallIntent(ctx, native, existing)
		}
		if existing.Thread == d.Thread && existing.ServerID == d.ServerID {
			return "unavailable", errors.New("recall server receipt belongs to another intent")
		}
	}
	if w.config.AuthorizeThread != nil {
		allowed, err := w.config.AuthorizeThread(ctx, p.Input.Thread)
		if err != nil || !allowed {
			return "unavailable", errors.New("recall conversation no longer authorized")
		}
	}
	messageID, err := w.findOutgoingRecallMessage(ctx, native, d)
	if err != nil {
		return "unavailable", err
	}
	// The original message and the current connection must still match after
	// directory discovery and before reserving the irreversible operation.
	message, err := w.source.Message(ctx, messageID)
	if err != nil || !sameRecallTarget(message, native.Account(), d.Thread, messageID, d.ServerID) {
		return "unavailable", errors.New("original outgoing message no longer matches delivery")
	}
	if err := w.beforeSubmit(ctx); err != nil {
		return "unavailable", err
	}
	key := recallKey(native.Account(), d.Thread, d.ServerID)
	intent := &recallIntent{DeliveryKey: d.Key, Thread: d.Thread, MessageID: messageID, ServerID: d.ServerID, Key: key, State: "unknown"}
	w.runtime.Recalls = append(w.runtime.Recalls, intent)
	if err := w.saveRuntime(); err != nil {
		w.runtime.Recalls = w.runtime.Recalls[:len(w.runtime.Recalls)-1]
		return "unavailable", err
	}
	action := linuxnative.AdminAction{Kind: "recall", ThreadID: d.Thread, MessageID: messageID, ServerID: d.ServerID}
	status, err := native.ExecuteAction(ctx, action, key)
	if errors.Is(err, linuxnative.ErrWriteNotAdmitted) {
		// Native explicitly guarantees that this write never crossed its guard.
		// Drop the reservation so a later authorized correction may try again.
		w.runtime.Recalls = w.runtime.Recalls[:len(w.runtime.Recalls)-1]
		if saveErr := w.saveRuntime(); saveErr != nil {
			return "unknown", saveErr
		}
		return "unavailable", err
	}
	if err != nil || status.ID == "" {
		// The request might have reached the native service. Do not POST again.
		w.activity(Record{Event: "message_recall_unknown", Category: "error", ThreadID: d.Thread, MessageID: p.Input.TraceID, Status: "unknown", Reason: "native_recall_receipt_unavailable"})
		return "unknown", nil
	}
	intent.OperationID = status.ID
	intent.State = recallReceiptState(status, native.Account(), action, status.ID)
	if err := w.saveRuntime(); err != nil {
		return "unknown", err
	}
	w.recordRecallOutcome(p, intent)
	return intent.State, nil
}

func (w *Worker) acceptedBubbleDelivery(p *utterancePlan, sentIndex int) (*delivery, error) {
	trace := p.Trace
	if trace == "" {
		trace = p.Input.TraceID
	}
	rows := make([]*delivery, 0, p.Index)
	for _, d := range w.runtime.Deliveries {
		if d != nil && d.Trace == trace && d.Thread == p.Input.Thread && d.State == "accepted" {
			rows = append(rows, d)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Bubble < rows[j].Bubble })
	if len(rows) != p.Index {
		return nil, errors.New("accepted delivery count does not match sent plan")
	}
	for i, d := range rows {
		if d.Key == "" || d.ID == "" || d.ServerID == "" || d.Text != p.Sent[i] ||
			(d.Input.Account != "" && d.Input.Account != w.source.Account()) ||
			i > 0 && rows[i-1].Bubble >= d.Bubble {
			return nil, errors.New("accepted delivery does not match sent bubble")
		}
	}
	return rows[sentIndex], nil
}

func (w *Worker) findOutgoingRecallMessage(ctx context.Context, native nativeRecallSender, d *delivery) (string, error) {
	cursor := ""
	seen := map[string]bool{}
	for {
		page, err := native.Catalog(ctx, "messages", d.Thread, "", cursor)
		if err != nil {
			return "", err
		}
		candidate := ""
		for _, row := range page.Items {
			if row["threadId"] != d.Thread || row["serverId"] != d.ServerID || row["direction"] != "outgoing" {
				continue
			}
			id, _ := row["id"].(string)
			if id == "" || len(id) > 8192 || candidate != "" && candidate != id {
				return "", errors.New("ambiguous native recall target")
			}
			candidate = id
		}
		if candidate != "" {
			return candidate, nil
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return "", errors.New("accepted message absent from native directory")
		}
		if seen[*page.NextCursor] || *page.NextCursor == cursor {
			return "", errors.New("native recall directory cursor repeated")
		}
		seen[*page.NextCursor] = true
		cursor = *page.NextCursor
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
}

func sameRecallTarget(message linuxnative.Message, account, thread, id, server string) bool {
	return message.AccountID == account && message.ThreadID == thread && message.ID == id &&
		message.ServerID == server && message.Direction == "outgoing"
}

func recallKey(account, thread, server string) string {
	hash := sha256.Sum256([]byte(account + "\x00" + thread + "\x00" + server))
	return "bot-recall-" + hex.EncodeToString(hash[:16])
}

func recallReceiptState(status linuxnative.ActionStatus, account string, action linuxnative.AdminAction, operation string) string {
	if status.ID == "" || status.ID != operation || status.AccountID != account || status.Kind != "message_recall" ||
		status.ThreadID != action.ThreadID || status.MessageID != action.MessageID || status.ServerID != action.ServerID {
		return "unknown"
	}
	switch status.State {
	case "succeeded":
		if status.RemoteOutcome == "accepted" || status.RemoteOutcome == "confirmed" {
			return "succeeded"
		}
		return "unknown"
	case "failed", "rejected", "cancelled":
		return "failed"
	default:
		return "pending"
	}
}

func (w *Worker) inspectRecallIntent(ctx context.Context, native nativeRecallSender, intent *recallIntent) (string, error) {
	if intent.State == "succeeded" || intent.State == "failed" || intent.State == "unknown" {
		return intent.State, nil
	}
	if intent.OperationID == "" {
		return "unknown", nil
	}
	status, err := native.InspectAction(ctx, intent.OperationID)
	if err != nil {
		return "pending", err
	}
	action := linuxnative.AdminAction{Kind: "recall", ThreadID: intent.Thread, MessageID: intent.MessageID, ServerID: intent.ServerID}
	state := recallReceiptState(status, native.Account(), action, intent.OperationID)
	if state != intent.State {
		intent.State = state
		if err := w.saveRuntime(); err != nil {
			return "pending", err
		}
		w.activity(Record{Event: "message_recall_status", Category: "decision", ThreadID: intent.Thread, Status: state, Reason: strings.TrimSpace(status.Code)})
	}
	return state, nil
}

func (w *Worker) recordRecallOutcome(p *utterancePlan, intent *recallIntent) {
	w.activity(Record{Event: "message_recall_status", Category: "decision", ThreadID: intent.Thread,
		MessageID: p.Input.TraceID, Status: intent.State, Reason: "native_recall_operation", SendID: intent.OperationID})
}
