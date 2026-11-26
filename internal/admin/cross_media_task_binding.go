package admin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func readCrossBatchFile(path, account string) (crossMediaBatch, error) {
	var batch crossMediaBatch
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &batch) != nil || !validID(batch.ID) || filepath.Base(path) != batch.ID+".json" {
		return batch, errors.New("cross_media_journal_unavailable")
	}
	return batch, validateCrossBatch(batch, account)
}

// A media operation can be queued before the Agent saves its continuation.
// Link only an operation from this exact originating request. The batch and
// worker seed keep their original identity; no media is submitted here.
func (a *agentControl) attachUnboundCrossMediaOperations(t *agentTask) {
	if t == nil || t.OriginTrace == "" || t.Account == "" || t.Sender == "" || t.Reminder != nil || t.State == "cancelled" || t.State == "stopped" {
		return
	}
	requestThread := t.RequestThread
	if requestThread == "" {
		requestThread = t.Thread
	}
	sourceThread := taskSourceThread(*t)
	entries, err := os.ReadDir(filepath.Join(a.dir, "cross-media"))
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(a.dir, "cross-media", entry.Name())
		batch, err := readCrossBatchFile(path, t.Account)
		if err != nil || batch.TaskID != "" || batch.OriginInput == nil || batch.OriginInput.OriginTraceID != t.OriginTrace || batch.Origin != requestThread || batch.Source != sourceThread || batch.Sender != t.Sender || !batch.CancelledAt.IsZero() || batch.SupersededBy != "" || crossBatchAccepted(batch) {
			continue
		}
		claimedElsewhere := false
		for id, existing := range a.data.Tasks {
			if id != t.ID && contains(existing.PendingOperations, batch.ID) {
				claimedElsewhere = true
				break
			}
		}
		if claimedElsewhere {
			continue
		}
		if !contains(t.PendingOperations, batch.ID) {
			t.PendingOperations = append(t.PendingOperations, batch.ID)
		}
	}
}
