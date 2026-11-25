package admin

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompressionJobsPersistAndRecoverInterrupted(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	m := &Manager{dir: dir, compressionJobs: map[string]*contextCompressionJob{
		"scope-running": {ScopeID: "scope-running", Trace: "compression-running", State: "running", Message: "正在调用模型", StartedAt: started, UpdatedAt: started},
		"scope-done":    {ScopeID: "scope-done", Trace: "compression-done", State: "completed", Changed: true, Message: "完成", StartedAt: started, UpdatedAt: started},
	}}
	m.compressionMu.Lock()
	if err := m.persistCompressionJobsLocked(); err != nil {
		t.Fatal(err)
	}
	m.compressionMu.Unlock()

	reopened := &Manager{dir: dir}
	if err := reopened.loadCompressionJobs(); err != nil {
		t.Fatal(err)
	}
	interrupted := reopened.compressionStatus("scope-running")
	if interrupted.State != "failed" || interrupted.Code != "context_compression_interrupted" || interrupted.Changed {
		t.Fatalf("running compression was not recovered safely: %+v", interrupted)
	}
	if interrupted.Message == "" || interrupted.StartedAt == "" || interrupted.UpdatedAt == "" {
		t.Fatalf("recovered status lost timestamps/message: %+v", interrupted)
	}
	completed := reopened.compressionStatus("scope-done")
	if completed.State != "completed" || !completed.Changed {
		t.Fatalf("completed compression was changed during recovery: %+v", completed)
	}

	data, err := os.ReadFile(filepath.Join(dir, contextCompressionJobsFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("recovery did not persist the interrupted terminal state")
	}
}

func TestCompressionJobsRejectCorruptStateWithoutTouchingMemory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, contextCompressionJobsFile), []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{dir: dir}
	if err := m.loadCompressionJobs(); err == nil {
		t.Fatal("corrupt compression state was silently accepted")
	}
	if m.compressionJobs != nil {
		t.Fatalf("corrupt state unexpectedly populated jobs: %#v", m.compressionJobs)
	}
}
