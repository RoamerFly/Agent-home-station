package adapter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
)

func TestLegacyReceiptRejectsMismatchedArchivedMediaSource(t *testing.T) {
	base := t.TempDir()
	tokenPath := filepath.Join(base, "native-token")
	if err := os.WriteFile(tokenPath, []byte("synthetic-legacy-receipt-token"), 0600); err != nil {
		t.Fatal(err)
	}
	var matchingSource, replaceGeneration, generationChanged atomic.Bool
	var requests atomic.Int32
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-legacy-receipt-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/v1/status":
			generation := "synthetic-generation"
			if generationChanged.Load() {
				generation = "different-process-same-dataset"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"pid": 44, "dataDir": base, "serverId": "legacy-media-dataset", "ownershipGeneration": generation})
		case "/v1/accounts/one/sends/old-media-operation":
			requests.Add(1)
			if replaceGeneration.Load() {
				generationChanged.Store(true)
			}
			returnedSource := "other-archived-source"
			if matchingSource.Load() {
				returnedSource = "expected-archived-source"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "old-media-operation", "accountId": "one", "threadId": "filehelper", "kind": "voice", "state": "accepted", "serverId": "77123", "sourceId": returnedSource})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer daemon.Close()
	native, err := linuxnative.New(linuxnative.Config{URL: daemon.URL, TokenFile: tokenPath, Account: "one"})
	if err != nil {
		t.Fatal(err)
	}
	const dataset, source = "legacy-media-dataset", "expected-archived-source"
	original := apphost.Receipt{
		ConnectionRef:  apphost.ConnectionRef{AccountID: "one"},
		ConversationID: "filehelper",
		DeliveryReceipt: apphost.DeliveryReceipt{
			IdempotencyKey: "legacy-media", OperationID: "old-media-operation", Kind: "voice", State: "submission_unknown", Lookup: "legacy_snapshot",
		},
	}
	checked := ReadArchivedDelivery(t.Context(), native, original, dataset, source)
	if checked.Lookup != "identity_mismatch" || checked.State != "submission_unknown" || checked.OperationID != original.OperationID || checked.ServerID != "" {
		t.Fatalf("mismatched source changed the archived receipt: %#v", checked)
	}
	if count := requests.Load(); count != 1 {
		t.Fatalf("unexpected lookup count after mismatch: %d", count)
	}
	matchingSource.Store(true)
	checked = ReadArchivedDelivery(t.Context(), native, original, dataset, source)
	if checked.Lookup != "platform" || checked.State != "accepted" || checked.ServerID != "77123" {
		t.Fatalf("matching source did not produce a bounded platform receipt: %#v", checked)
	}
	if count := requests.Load(); count != 2 {
		t.Fatalf("unexpected lookup count after matching source: %d", count)
	}
	replaceGeneration.Store(true)
	checked = ReadArchivedDelivery(t.Context(), native, original, dataset, source)
	if checked.Lookup != "legacy_native_unavailable" || checked.State != "submission_unknown" || checked.ServerID != "" || requests.Load() != 3 {
		t.Fatalf("replacement process accepted the old operation evidence: %#v", checked)
	}
}

func TestLegacyReceiptDoesNotLoseSegmentEvidence(t *testing.T) {
	receipt := apphost.DeliveryReceipt{
		OperationID: "old-job", State: "submission_unknown", Lookup: "legacy_snapshot", PartsIncomplete: true,
		AcceptedParts:    []apphost.ReceiptPart{{Index: 0, ServerID: "101"}},
		UnconfirmedParts: []apphost.ReceiptPart{{Index: 1, State: "unknown"}},
	}
	status := linuxnative.ToolStatus{ID: "old-job", State: "accepted", ServerID: "102", VoiceChunks: []linuxnative.VoiceChunkStatus{
		{Index: 0, State: "accepted", ServerID: "999", HasReceipt: true},
	}}
	if projectLegacyReceipt(&receipt, status) || receipt.AcceptedParts[0].ServerID != "101" || receipt.State != "submission_unknown" {
		t.Fatalf("conflicting platform part replaced archived evidence: %#v", receipt)
	}
	status.VoiceChunks[0].ServerID = "101"
	if !projectLegacyReceipt(&receipt, status) || receipt.State == "accepted" || !receipt.PartsIncomplete || len(receipt.UnconfirmedParts) != 1 {
		t.Fatalf("omitted segment was treated as accepted: %#v", receipt)
	}
	status.VoiceChunks = append(status.VoiceChunks, linuxnative.VoiceChunkStatus{Index: 1, State: "accepted", ServerID: "102", HasReceipt: true})
	if !projectLegacyReceipt(&receipt, status) || receipt.State != "accepted" || receipt.PartsIncomplete || len(receipt.AcceptedParts) != 2 || len(receipt.UnconfirmedParts) != 0 {
		t.Fatalf("complete platform evidence did not resolve the receipt: %#v", receipt)
	}
}
