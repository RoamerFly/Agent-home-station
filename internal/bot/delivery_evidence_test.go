package bot

import (
	"path/filepath"
	"testing"

	"wechat-bot/internal/companion"
)

func TestDeliveryEvidenceNeverAssignsEarlierReceiptToRetryKey(t *testing.T) {
	dir := t.TempDir()
	in := companion.Input{Account: "fixture", Thread: "room@chatroom"}
	row := &delivery{Key: "retry-key", ID: "earlier-job", ServerID: "123", State: "accepted", Thread: in.Thread,
		Input: in, PreviousKeys: []string{"original-key"}}
	snapshot := runtimeState{Version: 1, Account: in.Account, Deliveries: []*delivery{row}}
	path := filepath.Join(dir, "messages-v2.json")
	if err := atomicState(path, snapshot); err != nil {
		t.Fatal(err)
	}
	keys := []string{"original-key", "retry-key", "unrelated"}
	rows, err := ReadDeliveryEvidence(dir, in.Account, keys)
	if err != nil || len(rows) != 2 {
		t.Fatal("keys not preserved across retry", rows, err)
	}
	for _, key := range keys[:2] {
		if status := rows[key].Status; status.State != "unknown" || status.ID != "" || status.ServerID != "" || status.ThreadID != in.Thread {
			t.Fatal("ambiguous receipt assigned to an attempt", key, status)
		}
	}
	if _, err := ReadDeliveryEvidence(dir, "other-account", keys); err == nil {
		t.Fatal("foreign checkpoint was read")
	}
	snapshot.Deliveries = append(snapshot.Deliveries, row)
	if err := atomicState(path, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDeliveryEvidence(dir, in.Account, keys); err == nil {
		t.Fatal("duplicate attempt identity was resolved arbitrarily")
	}
}
