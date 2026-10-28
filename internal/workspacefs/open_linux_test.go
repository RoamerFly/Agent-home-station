//go:build linux

package workspacefs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestPipeCannotBlockBeforeOrAfterMetadataCheck(t *testing.T) {
	dir := t.TempDir()
	pipe := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, directory := range []bool{false, true} {
		// Calling the second stage directly represents replacement by a pipe
		// after the initial stat, without a probabilistic racing test loop.
		for _, afterStat := range []bool{false, true} {
			done := make(chan error, 1)
			go func() {
				var err error
				if afterStat {
					_, err = openChecked(root, "pipe", directory)
				} else {
					_, err = open(root, "pipe", directory)
				}
				done <- err
			}()
			select {
			case err := <-done:
				want := ErrNotRegular
				if directory {
					want = ErrNotDirectory
				}
				if !errors.Is(err, want) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("special-file open blocked")
			}
		}
	}
}
