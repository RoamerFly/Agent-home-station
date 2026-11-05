//go:build !windows

package companion

import (
	"errors"
	"os"
	"syscall"
)

func lockMemoryAccess(path string, exclusive bool) (*os.File, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	mode := syscall.LOCK_SH | syscall.LOCK_NB
	if exclusive {
		mode = syscall.LOCK_EX | syscall.LOCK_NB
	}
	if syscall.Flock(int(f.Fd()), mode) != nil {
		f.Close()
		return nil, errors.New("memory store is online or maintenance is active; stop the Web service before offline repair")
	}
	return f, nil
}
