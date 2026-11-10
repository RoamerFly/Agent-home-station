package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/linuxnative"
)

func TestProjectRollbackReceiptSeparatesActiveFromUnknownAndRetainsParts(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("synthetic-rollback-token"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-rollback-token" || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/accounts/one/sends/voice-job" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "voice-job", "accountId": "one", "threadId": "room", "kind": "voice", "state": "running", "sourceId": "source-one",
			"result": map[string]any{"chunks": []map[string]any{
				{"index": 0, "state": "accepted", "serverMessageId": "71", "durationMs": 1000},
				{"index": 1, "state": "submitting", "durationMs": 1000},
			}},
		})
	}))
	defer server.Close()
	native, err := linuxnative.New(linuxnative.Config{URL: server.URL, TokenFile: token, Account: "one"})
	if err != nil {
		t.Fatal(err)
	}
	row := bot.DeliveryEvidence{SourceID: "source-one", StrictSource: true, Status: linuxnative.ToolStatus{
		ID: "voice-job", Kind: "voice", ThreadID: "room", State: "unknown",
		VoiceChunks: []linuxnative.VoiceChunkStatus{
			{Index: 0, State: "accepted", HasReceipt: true, ServerID: "71"},
			{Index: 2, State: "submitting"},
		},
	}}
	ref := apphost.ConnectionRef{AppID: "wechat", InstallationID: "local", ConnectionID: "wechat-one", AccountID: "one"}
	receipt, active := ProjectRollbackReceipt(context.Background(), native, ref, "delivery-one", row)
	if !active || receipt.Lookup != "platform" || receipt.PlatformState != "running" || receipt.State != "submitted_pending" {
		t.Fatalf("running native operation was not projected as active: %#v active=%v", receipt, active)
	}
	if len(receipt.AcceptedParts) != 1 || receipt.AcceptedParts[0].Index != 0 || receipt.AcceptedParts[0].ServerID != "71" {
		t.Fatalf("accepted prefix was lost: %#v", receipt)
	}
	if len(receipt.UnconfirmedParts) != 2 || receipt.UnconfirmedParts[0].Index != 1 || receipt.UnconfirmedParts[1].Index != 2 {
		t.Fatalf("a native response that omitted a pending position erased evidence: %#v", receipt)
	}

	unknown, active := ProjectRollbackReceipt(context.Background(), nil, ref, "delivery-one", row)
	if active || unknown.Lookup != "stored" || unknown.State != "submission_unknown" {
		t.Fatalf("offline checkpoint must remain unknown rather than active: %#v active=%v", unknown, active)
	}
	if len(unknown.AcceptedParts) != 1 || unknown.AcceptedParts[0].ServerID != "71" {
		t.Fatalf("offline projection lost accepted prefix: %#v", unknown)
	}
}

func TestRollbackCheckpointPreservesSnapshotAndControlEvidence(t *testing.T) {
	dir := t.TempDir()
	queue := filepath.Join(dir, "queue")
	if err := os.Mkdir(queue, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queue, "messages-v2.json"), []byte(workerCheckpointFixture), 0600); err != nil {
		t.Fatal(err)
	}
	frozen := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	ref := apphost.ConnectionRef{AppID: ID, InstallationID: "local", ConnectionID: ConnectionID("fixture"), AccountID: "fixture"}
	journal := DeliveryJournal(dir)
	original, err := ReadRollbackCheckpoint(journal, ref, frozen, "original-server", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	// Compare with the legacy station digest encoding for already sealed audits.
	raw, err := json.Marshal([]any{"wechat-rollback-v1", ref, frozen, "original-server", original.rows.Snapshot, map[string]DeliveryStop{}, map[string]ResumeControl{}})
	if err != nil || original.Snapshot() != fmt.Sprintf("%x", sha256.Sum256(raw)) {
		t.Fatal("legacy rollback snapshot changed", original.Snapshot(), err)
	}
	if err := journal.SaveStop(DeliveryStop{Account: "fixture", Thread: "room", Trace: "original-turn", Key: "original-key", At: frozen}); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.RequestResume("fixture", "original-key", "request-one", "room", "original-turn", map[string]ResumeControl{}, frozen); err != nil {
		t.Fatal(err)
	}
	first, err := ReadRollbackCheckpoint(journal, ref, frozen, "original-server", "", 1)
	if err != nil || first.Snapshot() == original.Snapshot() || first.Dataset() != "original-server" {
		t.Fatal("the checkpoint omitted journal changes", first.Snapshot(), err)
	}
	last, err := ReadRollbackCheckpoint(journal, ref, frozen, "original-server", "original-key", 1)
	if err != nil || !last.Empty() || last.Snapshot() != first.Snapshot() {
		t.Fatal("page boundaries changed the sealed snapshot", last.Snapshot(), err)
	}
	rows, err := first.Receipts(t.Context(), nil)
	if err != nil || len(rows) != 1 || !rows[0].CancelRequested || rows[0].Continuation == nil ||
		rows[0].IdempotencyKey != "original-key" || rows[0].Lookup != "stored" {
		t.Fatal("the offline page lost stop or resume evidence", rows, err)
	}
}
