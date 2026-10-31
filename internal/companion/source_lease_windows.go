//go:build windows

package companion

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var memoryLockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func lockMemoryAccess(path string, exclusive bool) (*os.File, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	flags := uintptr(1)
	if exclusive {
		flags |= 2
	}
	var overlapped syscall.Overlapped
	ok, _, _ := memoryLockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 {
		f.Close()
		return nil, errors.New("memory store is online or maintenance is active; stop the Web service before offline repair")
	}
	return f, nil
}

// An old Web binary does not hold the new sidecar lease. Windows sharing
// admission also rejects that legacy process while its database handle is open.
func verifyLegacyMemoryOffline(path string) error {
	if _, e := os.Stat(path); os.IsNotExist(e) {
		return nil
	} else if e != nil {
		return e
	}
	p, e := syscall.UTF16PtrFromString(path)
	if e != nil {
		return e
	}
	h, e := syscall.CreateFile(p, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if e != nil {
		return errors.New("memory database is open in another process; stop all Web instances before offline repair")
	}
	return syscall.CloseHandle(h)
}
