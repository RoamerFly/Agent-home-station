//go:build windows

package bot

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func lockState(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, errors.New("cannot open bot state lock")
	}
	var overlapped syscall.Overlapped
	ok, _, _ := lockFileEx.Call(f.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 {
		f.Close()
		return nil, errors.New("another bot owns this state directory")
	}
	return f, nil
}

// Rename/fsync failures stop processing. Windows does not expose a portable
// directory fsync; deployment with strict crash durability uses Linux.
func syncDirectory(string) error { return nil }
