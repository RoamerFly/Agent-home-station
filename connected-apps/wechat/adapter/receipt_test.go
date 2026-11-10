package adapter

import (
	"os"
	"path/filepath"
	"testing"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
)

func TestWorkerContinuationReceiptCannotBorrowOriginalJob(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("synthetic-continuation-receipt-token"), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := linuxnative.New(linuxnative.Config{URL: "http://127.0.0.1:1", TokenFile: token, Account: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	reader := workerMediaReceiptReader{native: native, source: "source", kind: "voice", origin: "original-job"}
	value := linuxnative.ToolStatus{ID: "original-job", AccountID: "fixture", SourceID: "source", Kind: "send", State: "accepted", ServerID: "100"}
	if _, err := reader.validate(value, nil); err == nil {
		t.Fatal("original accepted receipt assigned to continuation")
	}
	value.ID = "new-job"
	if _, err := reader.validate(value, nil); err != nil {
		t.Fatal("valid independent continuation rejected", err)
	}
}

func TestApplicationReceiptRetainsAcceptedPartsWhenRemainingSendIsCancelled(t *testing.T) {
	part := linuxnative.VoiceChunkStatus{Index: 0, State: "accepted", HasReceipt: true, ServerID: "111"}
	status := linuxnative.ToolStatus{ID: "same-job", State: "needs_review", ServerID: "111", VoiceChunks: []linuxnative.VoiceChunkStatus{part}}
	var result apphost.DeliveryReceipt
	ProjectDeliveryReceipt(&result, status)
	if result.State != "submission_unknown" || result.ServerID != "" || len(result.AcceptedParts) != 1 {
		t.Fatal("one accepted part was mistaken for a complete delivery", result)
	}
	status.State, status.VoiceChunks = "cancelled", nil
	ProjectDeliveryReceipt(&result, status)
	if result.State != "cancelled" || result.ServerID != "" || len(result.AcceptedParts) != 1 || result.AcceptedParts[0].ServerID != "111" {
		t.Fatal("cancel discarded an existing receipt", result)
	}
	status.State, status.VoiceChunks = "accepted", []linuxnative.VoiceChunkStatus{part, {Index: 1, State: "submitting"}}
	ProjectDeliveryReceipt(&result, status)
	if result.State == "accepted" || result.ServerID != "" {
		t.Fatal("in-flight second segment was treated as fully accepted", result)
	}
}
