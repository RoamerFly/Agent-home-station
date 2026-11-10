package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
)

// ProjectRollbackReceipt builds a read-only rollback record for one durable
// delivery checkpoint.  It never schedules, retries, resumes, or cancels a
// send.  active is true only when the native service has been contacted and
// currently reports that same operation as actively queued/submitting; an
// unknown receipt is deliberately not treated as active.
//
// The adapter projects the stored status before looking up the native job, so
// verified accepted voice parts survive a later stale or partial result.
func ProjectRollbackReceipt(ctx context.Context, native *linuxnative.Sender, ref apphost.ConnectionRef, key string, row bot.DeliveryEvidence) (receipt apphost.Receipt, active bool) {
	receipt = StoredWorkerReceipt(ref, key, row)

	// A missing current transport is a valid offline rollback condition: retain
	// the checkpoint projection as durable evidence, but do not invent a native
	// state or classify it as still active.
	if native == nil {
		return receipt, false
	}
	priorPending := append([]apphost.ReceiptPart(nil), receipt.UnconfirmedParts...)
	priorIncomplete := receipt.PartsIncomplete

	LookupWorkerReceipt(ctx, native, row, &receipt.DeliveryReceipt)
	if receipt.DeliveryReceipt.Lookup != "platform" {
		return receipt, false
	}
	// A later status may report fewer chunks than the checkpoint. Retain the
	// missing pending positions unless this same lookup confirmed them.
	seen := make(map[int]bool, len(receipt.AcceptedParts)+len(receipt.UnconfirmedParts))
	for _, part := range receipt.AcceptedParts {
		seen[part.Index] = true
	}
	for _, part := range receipt.UnconfirmedParts {
		seen[part.Index] = true
	}
	for _, part := range priorPending {
		if !seen[part.Index] {
			receipt.UnconfirmedParts = append(receipt.UnconfirmedParts, part)
		}
	}
	if receipt.State == "accepted" && len(receipt.UnconfirmedParts) != 0 {
		receipt.State, receipt.ServerID = "submission_unknown", ""
	}
	if priorIncomplete && receipt.State != "accepted" {
		receipt.PartsIncomplete = true
	}
	switch receipt.DeliveryReceipt.PlatformState {
	case "queued", "running", "sending", "submitting":
		return receipt, true
	default:
		return receipt, false
	}
}

type RollbackReceipt struct {
	apphost.Receipt
	Disposition string `json:"disposition"`
}

// RollbackCheckpoint captures the durable state used for both rollback pages
// and the sealed audit. The host holds its delivery lock during capture.
type RollbackCheckpoint struct {
	ref           apphost.ConnectionRef
	targetDataset string
	rows          bot.DeliveryEvidencePage
	stops         map[string]DeliveryStop
	controls      map[string]ResumeControl
	snapshot      string
}

func ReadRollbackCheckpoint(journal DeliveryJournal, ref apphost.ConnectionRef, frozenAt time.Time, targetDataset, after string, limit int) (RollbackCheckpoint, error) {
	rows, err := ReadWorkerDeliveryPage(string(journal), ref.AccountID, after, limit)
	stops, stopErr := journal.ReadStops()
	controls, controlErr := journal.ReadResumes()
	if err != nil || stopErr != nil || controlErr != nil {
		return RollbackCheckpoint{}, apphost.ErrUnavailable
	}
	// Keep the legacy digest inputs and JSON order so saved audits still match.
	raw, err := json.Marshal([]any{"wechat-rollback-v1", ref, frozenAt, targetDataset, rows.Snapshot, stops, controls})
	if err != nil {
		return RollbackCheckpoint{}, apphost.ErrUnavailable
	}
	digest := sha256.Sum256(raw)
	return RollbackCheckpoint{ref, targetDataset, rows, stops, controls, hex.EncodeToString(digest[:])}, nil
}

func (c RollbackCheckpoint) Snapshot() string  { return c.snapshot }
func (c RollbackCheckpoint) Dataset() string   { return c.rows.ServerID }
func (c RollbackCheckpoint) NextAfter() string { return c.rows.NextAfter }
func (c RollbackCheckpoint) Empty() bool       { return len(c.rows.Keys) == 0 }

// VerifyRollbackTransport rejects a current native session belonging to
// another account or dataset before any platform delivery evidence is read.
func VerifyRollbackTransport(ctx context.Context, ref apphost.ConnectionRef, dataset string, sender func(context.Context, string) (*linuxnative.Sender, error)) (*linuxnative.Sender, linuxnative.DaemonStatus, string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	native, err := sender(ctx, ref.ConnectionID)
	if err != nil || native == nil || native.Account() != ref.AccountID {
		return nil, linuxnative.DaemonStatus{}, "unavailable"
	}
	daemon, err := native.DaemonStatus(ctx)
	if err != nil {
		return nil, daemon, "unavailable"
	}
	if daemon.ServerID != dataset {
		return nil, daemon, "dataset_mismatch"
	}
	return native, daemon, "available"
}

// Receipts combines one local page with optional platform evidence. Only a
// transport verified for the target dataset may contribute platform results.
func (c RollbackCheckpoint) Receipts(ctx context.Context, native *linuxnative.Sender) ([]RollbackReceipt, error) {
	ref := c.ref
	if c.rows.ServerID != c.targetDataset || (native != nil && native.Account() != ref.AccountID) {
		native = nil
	}
	items := make([]RollbackReceipt, 0, len(c.rows.Keys))
	for _, key := range c.rows.Keys {
		if !identity.ValidPathID(key) {
			return nil, apphost.ErrUnavailable
		}
		// Keep the requested local page intact if the optional platform lookups
		// exhaust their deadline. The remaining records retain stored evidence.
		lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		row := c.rows.Records[key]
		receipt, active := ProjectRollbackReceipt(lookupCtx, native, ref, key, row)
		cancel()
		_, stopped := c.stops[DeliveryStopID(companion.Input{Account: ref.AccountID, Thread: row.Status.ThreadID, TraceID: row.Trace})]
		receipt.CancelRequested = receipt.CancelRequested || stopped
		if control, exists := c.controls[key]; exists {
			receipt.Continuation = ResumeControlView(control)
		}
		items = append(items, RollbackReceipt{Receipt: receipt, Disposition: rollbackDisposition(receipt, active)})
	}
	return items, nil
}

func rollbackDisposition(receipt apphost.Receipt, active bool) string {
	switch {
	case active:
		return "active"
	case receipt.State == "accepted":
		return "accepted"
	case receipt.PartsIncomplete || len(receipt.UnconfirmedParts) != 0:
		return "unknown"
	case receipt.State == "failed" || receipt.State == "cancelled":
		return "terminal"
	case receipt.State == "not_submitted" && receipt.OperationID == "":
		return "not_submitted"
	default:
		return "unknown"
	}
}
