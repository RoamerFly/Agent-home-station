//go:build windows

package devicebroker

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var (
	errLeaseBusy = errors.New("physical device lease busy")
	lockFileEx   = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")
)

// physicalLease is an OS-owned LockFileEx lock. Windows releases it when the
// owning process exits, including a crash, so no stale timestamp can unblock a
// device while an earlier action might still be running.
type physicalLease struct{ file *os.File }

func acquireLease(path string) (*physicalLease, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	var overlapped syscall.Overlapped
	// LOCKFILE_FAIL_IMMEDIATELY | LOCKFILE_EXCLUSIVE_LOCK
	ok, _, _ := lockFileEx.Call(file.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 {
		_ = file.Close()
		return nil, errLeaseBusy
	}
	return &physicalLease{file: file}, nil
}

func (lease *physicalLease) Close() error {
	if lease == nil || lease.file == nil {
		return nil
	}
	err := lease.file.Close()
	lease.file = nil
	return err
}
