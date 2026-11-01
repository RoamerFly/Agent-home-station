//go:build linux

package admin

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRepairNonRegularFixtureCannotBlockController(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = syscall.Mkfifo(filepath.Join(dir, "fixture.py"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() {
		done <- repairFileHash(root, "fixture.py") == "" && createRepairFixture(root, "fixture.py", "new content") != nil
	}()
	select {
	case valid := <-done:
		if !valid {
			t.Fatal("nonregular fixture accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("controller blocked on an Agent-created FIFO")
	}
}
