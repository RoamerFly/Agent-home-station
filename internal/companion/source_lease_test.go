package companion

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
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
	mode := os.Getenv("WECHAT_MEMORY_LEASE_CHILD_MODE")
	if mode == "lease-shared" || mode == "lease-exclusive" {
		lease, e := lockMemoryAccess(filepath.Join(dir, ".lease-only.lock"), mode == "lease-exclusive")
		if e != nil {
			os.Exit(9)
		}
		if e = lease.Close(); e != nil {
			os.Exit(10)
		}
		os.Exit(0)
	}
	var s *Store
	var e error
	if mode == "offline" {
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

func TestMemoryAccessLeaseCrossProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".lease-only.lock")
	shared, e := lockMemoryAccess(path, false)
	if e != nil {
		t.Fatal(e)
	}
	if code := leaseChild(t, dir, "lease-shared"); code != 0 {
		t.Fatal("shared lease rejected another shared holder", code)
	}
	if code := leaseChild(t, dir, "lease-exclusive"); code != 9 {
		t.Fatal("exclusive lease admitted while shared lease held", code)
	}
	if e = shared.Close(); e != nil {
		t.Fatal(e)
	}

	exclusive, e := lockMemoryAccess(path, true)
	if e != nil {
		t.Fatal(e)
	}
	if code := leaseChild(t, dir, "lease-shared"); code != 9 {
		t.Fatal("shared lease admitted while exclusive lease held", code)
	}
	if e = exclusive.Close(); e != nil {
		t.Fatal(e)
	}
	if code := leaseChild(t, dir, "lease-exclusive"); code != 0 {
		t.Fatal("exclusive lease was not released", code)
	}
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
		if runtime.GOOS == "linux" && (errors.Is(e, syscall.EACCES) || errors.Is(e, syscall.EPERM)) {
			t.Logf("procfs denied a same-user descriptor read; offline maintenance safely refused: %v", e)
			if reopened, reopenErr := NewStore(dir); reopenErr != nil {
				t.Fatalf("offline refusal left its lease held: %v", reopenErr)
			} else if closeErr := reopened.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			return
		}
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
