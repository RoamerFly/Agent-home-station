package companion

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestVerifiedImportDoesNotRefreshInteractionToImportTime(t *testing.T) {
	s, c, in := memoryFixture(t)
	now := time.Now().UTC()
	in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), Integrity: "verified_import"}
	for i, delta := range []time.Duration{-4 * time.Hour, -3 * time.Hour, -5 * time.Hour} {
		at := now.Add(delta)
		in.TraceID = string(rune('a' + i))
		in.Source.MessageID = in.TraceID
		in.Source.OccurredAt = at.Format(time.RFC3339Nano)
		in.MessageTime = in.Source.OccurredAt
		in.Text = "historical verified message"
		if err := s.Observe(in, c, now.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		scope := s.Context(in, c, now)
		want := now.Add(-4 * time.Hour)
		if i > 0 {
			want = now.Add(-3 * time.Hour)
		}
		if !scope.LastInteraction.Equal(want) {
			t.Fatal("import refreshed social timers", scope.LastInteraction, want)
		}
	}
}
func TestMemoryLeaseChild(t *testing.T) {
	dir := os.Getenv("WECHAT_MEMORY_LEASE_CHILD_DIR")
	if dir == "" {
		return
	}
	var s *Store
	var e error
	if os.Getenv("WECHAT_MEMORY_LEASE_CHILD_MODE") == "offline" {
		s, e = NewOfflineStore(dir)
	} else {
		s, e = NewStore(dir)
	}
	if e != nil {
		os.Exit(9)
	}
	if e = s.Close(); e != nil {
		os.Exit(10)
	}
	os.Exit(0)
}
func leaseChild(t *testing.T, dir, mode string) int {
	t.Helper()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(exe, "-test.run=^TestMemoryLeaseChild$")
	cmd.Env = append(os.Environ(), "WECHAT_MEMORY_LEASE_CHILD_DIR="+dir, "WECHAT_MEMORY_LEASE_CHILD_MODE="+mode)
	if e = cmd.Run(); e != nil {
		if exit, ok := e.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		t.Fatal(e)
	}
	return 0
}
func TestMemoryMaintenanceLeaseRejectsOnlineProcesses(t *testing.T) {
	dir := t.TempDir()
	live, e := NewStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	if code := leaseChild(t, dir, "offline"); code != 9 {
		t.Fatal("offline repair admitted while live", code)
	}
	if _, e := AuditSources(t.Context(), dir); e != nil {
		t.Fatal("read-only audit blocked", e)
	}
	live.Close()
	maintenance, e := NewOfflineStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	if code := leaseChild(t, dir, "live"); code != 9 {
		t.Fatal("live store admitted during maintenance", code)
	}
	maintenance.Close()
	if code := leaseChild(t, dir, "offline"); code != 0 {
		t.Fatal("closed lease was not released", code)
	}
}

func TestOfflineLeaseRejectsLegacyDatabaseHandle(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("legacy handle check needs Windows or Linux")
	}
	dir := t.TempDir()
	s, e := NewStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
	legacy, e := sql.Open("sqlite", filepath.Join(dir, "memory.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer legacy.Close()
	var n int
	if e = legacy.QueryRow("SELECT count(*) FROM events").Scan(&n); e != nil {
		t.Fatal(e)
	}
	if offline, e := NewOfflineStore(dir); e == nil {
		offline.Close()
		t.Fatal("legacy online database handle was not detected")
	}
}
