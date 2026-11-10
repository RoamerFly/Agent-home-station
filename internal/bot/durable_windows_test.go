package bot

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"wechat-bot/internal/companion"
)

func TestQueueCheckpointReplacementWaitsForReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages-v2.json")
	before := runtimeState{Version: 1, Account: "fixture", Server: "synthetic-server"}
	if err := atomicState(path, before); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	timer := time.AfterFunc(150*time.Millisecond, func() { released <- reader.Close() })
	defer timer.Stop()

	after := before
	after.Deliveries = []*delivery{{
		Key: "original-key", ID: "original-job", State: "accepted", Thread: "filehelper",
		Input: companion.Input{Account: before.Account, Thread: "filehelper"},
	}}
	writeErr := atomicState(path, after)
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	records, err := ReadDeliveryEvidence(filepath.Dir(path), before.Account, []string{"original-key"})
	if receipt := records["original-key"].Status; err != nil || receipt.State != "accepted" || receipt.ID != "original-job" {
		t.Fatalf("replacement lost the original receipt: %+v, %v", receipt, err)
	}
}
