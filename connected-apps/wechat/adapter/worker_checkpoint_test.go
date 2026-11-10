package adapter

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"wechat-bot/internal/apphost"
)

const workerCheckpointFixture = `{"version":2,"account":"fixture","server":"original-server","media":[{"key":"original-key","trace":"original-turn","kind":"voice","state":"resume_prepared","resumed_from":"previous-key","input":{"account":"fixture","thread":"room","text":"private text"}}]}`

func TestWorkerCheckpointKeepsAccountAndContinuationScoped(t *testing.T) {
	runtimeDir := t.TempDir()
	if _, err := WorkerCheckpointAccount(runtimeDir, ConnectionID("fixture")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing checkpoint should allow archive fallback", err)
	}
	queue := filepath.Join(runtimeDir, "queue")
	if err := os.Mkdir(queue, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(queue, "messages-v2.json")
	if err := os.WriteFile(file, []byte(workerCheckpointFixture), 0600); err != nil {
		t.Fatal(err)
	}
	if account, err := WorkerCheckpointAccount(runtimeDir, ConnectionID("fixture")); err != nil || account != "fixture" {
		t.Fatal("same account was not recognized", account, err)
	}
	if _, err := WorkerCheckpointAccount(runtimeDir, ConnectionID("other")); !errors.Is(err, apphost.ErrUnavailable) {
		t.Fatal("other connection accessed the local checkpoint", err)
	}
	if _, _, err := ReadWorkerDelivery(runtimeDir, "other", "original-key"); err == nil {
		t.Fatal("another account could read original delivery")
	}
	page, err := ReadWorkerDeliveryPage(runtimeDir, "fixture", "", 1)
	if err != nil || len(page.Keys) != 1 || page.Snapshot == "" || page.ServerID != "original-server" {
		t.Fatal("queue projection lost its original provenance", page, err)
	}
	row, found, err := ReadWorkerDelivery(runtimeDir, "fixture", page.Keys[0])
	if err != nil || !found || row.ResumedFrom != "previous-key" || row.Status.ThreadID != "room" || row.Trace != "original-turn" {
		t.Fatal("single lookup lost the original continuation", row, found, err)
	}
	if err := os.WriteFile(file, []byte(workerCheckpointFixture+` {}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := WorkerCheckpointAccount(runtimeDir, ConnectionID("fixture")); !errors.Is(err, apphost.ErrUnavailable) {
		t.Fatal("an invalid checkpoint was admitted for offline operations", err)
	}
}
