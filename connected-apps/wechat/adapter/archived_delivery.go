package adapter

import (
	"context"
	"errors"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
)

// ReadArchivedDelivery reconciles only the original operation on its archived
// native dataset. The caller owns the archive and decides when to consult it.
func ReadArchivedDelivery(ctx context.Context, native *linuxnative.Sender, receipt apphost.Receipt, datasetID, source string) apphost.Receipt {
	if native == nil {
		receipt.Lookup = "legacy_native_unavailable"
		return receipt
	}
	if native.Account() != receipt.AccountID {
		receipt.Lookup = "identity_mismatch"
		return receipt
	}
	dataset, err := native.DaemonStatus(ctx)
	if err != nil {
		receipt.Lookup = "legacy_native_unavailable"
		return receipt
	}
	if dataset.ServerID != datasetID {
		receipt.Lookup = "legacy_dataset_mismatch"
		return receipt
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	status, err := native.ToolSendStatus(ctx, receipt.OperationID)
	switch {
	case errors.Is(err, linuxnative.ErrToolNotFound):
		receipt.Lookup = "not_found"
	case err != nil:
		receipt.Lookup = "unavailable"
	case status.ID != receipt.OperationID || status.ThreadID != receipt.ConversationID ||
		status.Kind != "" && status.Kind != "send" && status.Kind != receipt.Kind || source != "" && status.SourceID != source:
		receipt.Lookup = "identity_mismatch"
	default:
		if !SameNativeOwnership(ctx, native, dataset) {
			receipt.Lookup = "legacy_native_unavailable"
		} else if projectLegacyReceipt(&receipt.DeliveryReceipt, status) {
			receipt.Lookup = "platform"
		} else {
			receipt.Lookup = "identity_mismatch"
		}
	}
	return receipt
}

// A later observation cannot erase confirmed parts or report a conflicting
// part ID as success. Preserve old unconfirmed parts absent from the reply.
func projectLegacyReceipt(receipt *apphost.DeliveryReceipt, status linuxnative.ToolStatus) bool {
	chunks := make(map[int]linuxnative.VoiceChunkStatus, len(status.VoiceChunks))
	for _, chunk := range status.VoiceChunks {
		if _, duplicate := chunks[chunk.Index]; duplicate {
			return false
		}
		chunks[chunk.Index] = chunk
	}
	for _, part := range receipt.AcceptedParts {
		chunk, found := chunks[part.Index]
		if found && chunk.HasReceipt && chunk.ServerID != part.ServerID {
			return false
		}
		if !found || !chunk.HasReceipt {
			status.VoicePartsIncomplete = true
		}
	}
	var omitted []apphost.ReceiptPart
	for _, part := range receipt.UnconfirmedParts {
		if _, found := chunks[part.Index]; !found {
			status.VoicePartsIncomplete = true
			omitted = append(omitted, part)
		}
	}
	if receipt.PartsIncomplete && len(chunks) == 0 {
		status.VoicePartsIncomplete = true
	}
	ProjectDeliveryReceipt(receipt, status)
	receipt.UnconfirmedParts = append(receipt.UnconfirmedParts, omitted...)
	return true
}
