package adapter

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadLegacyWorkerCheckpointKeepsRawFilesAndReceiptMeaning(t *testing.T) {
	dir := filepath.Join("bot-state", "accounts", "fixture")
	queuePath, cursorPath := filepath.Join(dir, "messages-v2.json"), filepath.Join(dir, "checkpoint.json")
	queue := []byte(`{"version":2,"server":"fixture-dataset","account":"one","inbox":[{"phase":"received"}],"deliveries":[{"key":"fixture-key","thread":"fixture-room","state":"prepared","submitted_text":"synthetic body","input":{"account":"one","thread":"fixture-room"}}]}`)
	cursor := []byte(`{"version":1,"server_id":"fixture-dataset","account":"one","cursor":"7"}`)
	read := func(path string, limit int64) ([]byte, error) {
		switch path {
		case queuePath:
			if limit != 24<<20 {
				t.Fatal("queue limit changed", limit)
			}
			return queue, nil
		case cursorPath:
			if limit != 128<<10 {
				t.Fatal("cursor limit changed", limit)
			}
			return cursor, nil
		default:
			t.Fatal("unexpected read", path)
			return nil, os.ErrNotExist
		}
	}
	result, err := ReadLegacyWorkerCheckpoint(dir, "one", read)
	if err != nil || result.ServerID != "fixture-dataset" || result.Queue.Path != queuePath || result.Cursor.Path != cursorPath ||
		!bytes.Equal(result.Queue.Data, queue) || !bytes.Equal(result.Cursor.Data, cursor) {
		t.Fatalf("checkpoint files were altered: %+v, %v", result, err)
	}
	if row, ok := result.Deliveries["fixture-key"]; !ok || row.Status.State != "unknown" || row.Status.ThreadID != "fixture-room" {
		t.Fatalf("prepared submission did not retain its uncertain status: %+v", row)
	}
	pending, err := result.PendingWork()
	if err != nil || pending.Inbox != 1 || pending.Deliveries != 1 {
		t.Fatal(pending, err)
	}
	cursor = bytes.Replace(cursor, []byte("fixture-dataset"), []byte("different-dataset"), 1)
	if _, err := ReadLegacyWorkerCheckpoint(dir, "one", read); err == nil {
		t.Fatal("mismatched dataset accepted")
	}
	cursor = nil
	queue = bytes.Replace(queue, []byte("fixture-key"), []byte("../unsafe"), 1)
	if _, err := ReadLegacyWorkerCheckpoint(dir, "one", read); err == nil {
		t.Fatal("unsafe delivery key accepted")
	}
}

func TestReadLegacyWorkerCheckpointPreservesOptionalFilesAndGuardErrors(t *testing.T) {
	guard := errors.New("fixture manifest changed")
	read := func(path string, _ int64) ([]byte, error) {
		if filepath.Base(path) == "checkpoint.json" {
			return nil, guard
		}
		return nil, os.ErrNotExist
	}
	_, err := ReadLegacyWorkerCheckpoint("bot-state", "one", read)
	var failed *LegacyCheckpointReadError
	if !errors.As(err, &failed) || !failed.Cursor || !errors.Is(err, guard) {
		t.Fatalf("cursor guard error lost: %v", err)
	}
	result, err := ReadLegacyWorkerCheckpoint("bot-state", "one", func(string, int64) ([]byte, error) {
		return nil, os.ErrNotExist
	})
	if err != nil || result.ServerID != "" || result.Queue.Data != nil || len(result.Deliveries) != 0 {
		t.Fatalf("missing optional files became an import error: %+v, %v", result, err)
	}
}
