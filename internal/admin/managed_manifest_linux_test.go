//go:build linux

package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestProjectReadRejectsPipeWithoutWaitingForWriter(t *testing.T) {
	_, p := trustedAgent(t)
	pipe := filepath.Join(p.executionContext().Public, "manifests", "blocked.json")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := p.Call(context.Background(), "project_read", json.RawMessage(`{"id":"blocked"}`))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("special file accepted as manifest")
		}
	case <-time.After(time.Second):
		// Release the old blocking implementation before failing the test.
		writer, err := os.OpenFile(pipe, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		writer.WriteString(`{"id":"blocked"}`)
		writer.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("manifest reader did not release")
		}
		t.Fatal("project_read blocked opening a FIFO; cancellation cannot interrupt that open")
	}
}
