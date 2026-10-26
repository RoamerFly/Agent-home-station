package agenthost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProcessOutputRetainsLatestFailureTail(t *testing.T) {
	b := &limitedOutput{limit: 8, keepTail: true}
	b.Write([]byte("old-output"))
	b.Write([]byte("\nERROR!"))
	if len(b.data) != 8 || !strings.HasSuffix(string(b.data), "\nERROR!") || !b.truncated {
		t.Fatal(string(b.data), b.truncated)
	}
}

func TestBackgroundProcessStreamsIsScopedAndDoesNotReplay(t *testing.T) {
	root := t.TempDir()
	j, e := OpenJobs(filepath.Join(root, "jobs"))
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	c := Defaults()
	c.Mode = "full"
	c.Roots = []string{root}
	cmd := "printf 'started\\n'; sleep .2; printf 'done'"
	if runtime.GOOS == "windows" {
		cmd = "Write-Output 'started'; Start-Sleep -Milliseconds 200; Write-Output 'done'"
	}
	p, e := j.Start(c, "scope-a", "same-request", root, cmd, 10, nil)
	if e != nil || p.ID == "" {
		t.Fatal(p, e)
	}
	same, e := j.Start(c, "scope-a", "same-request", root, cmd, 10, nil)
	if e != nil || same.ID != p.ID {
		t.Fatal("reexecuted", same, e)
	}
	if _, e = j.Status("scope-b", p.ID, false); e == nil {
		t.Fatal("cross-scope process access")
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		p, e = j.Status("scope-a", p.ID, false)
		if p.State != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e != nil || p.State != "completed" || !strings.Contains(p.Output, "done") {
		t.Fatal(p, e)
	}
	j.Close()
	restored, e := OpenJobs(filepath.Join(root, "jobs"))
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	same, e = restored.Start(c, "scope-a", "same-request", root, cmd, 10, nil)
	if e != nil || same.ID != p.ID || same.State != "completed" {
		t.Fatal(same, e)
	}
	if _, e = restored.Start(c, "scope-a", "same-request", root, "different command", 10, nil); e == nil {
		t.Fatal("changed command accepted same key")
	}
	restored.Close()
	// Simulate a crash checkpoint without leaving an actual orphan process.
	same.State = "running"
	raw, _ := json.Marshal(same)
	if e = os.WriteFile(filepath.Join(root, "jobs", same.ID+".json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	restarted, e := OpenJobs(filepath.Join(root, "jobs"))
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close()
	old, e := restarted.Start(c, "scope-a", "same-request", root, cmd, 10, nil)
	if e != nil || old.State != "interrupted" || old.ID != same.ID {
		t.Fatal("restart replayed unknown command", old, e)
	}
}
func TestBackgroundPermissionRevocationCancelsWithoutReplay(t *testing.T) {
	root := t.TempDir()
	j, _ := OpenJobs(filepath.Join(root, "jobs"))
	defer j.Close()
	c := Defaults()
	c.Mode = "full"
	c.Roots = []string{root}
	var allow atomic.Bool
	allow.Store(true)
	cmd := "sleep 30"
	if runtime.GOOS == "windows" {
		cmd = "Start-Sleep -Seconds 30"
	}
	p, e := j.Start(c, "scope", "cancel", root, cmd, 60, allow.Load)
	if e != nil {
		t.Fatal(e)
	}
	allow.Store(false)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		p, _ = j.Status("scope", p.ID, false)
		if p.State != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p.State != "cancelled" {
		t.Fatal(p)
	}
}
func TestProjectEditRejectsStaleHashAndReturnsNewHash(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "program.py")
	os.WriteFile(path, []byte("print(21)\n"), 0600)
	c := Defaults()
	c.Mode = "files"
	c.Roots = []string{root}
	h := Host{c}
	v, e := invoke(t, h, "server_read", map[string]string{"path": path})
	if e != nil {
		t.Fatal(e)
	}
	sum := v.(map[string]any)["sha256"].(string)
	args := Input{Path: path, Expected: sum, Old: "21", New: "42"}
	b, _ := json.Marshal(args)
	if _, e = h.Call(context.Background(), "server_edit", b); e != nil {
		t.Fatal(e)
	}
	if _, e = h.Call(context.Background(), "server_edit", b); e == nil {
		t.Fatal("stale edit overwrote file")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "print(42)\n" {
		t.Fatal(string(data))
	}
}
