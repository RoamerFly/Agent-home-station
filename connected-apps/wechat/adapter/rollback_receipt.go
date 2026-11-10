package adapter

import (
	"context"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
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
