//go:build !windows

package devicebroker

import (
	"errors"
	"os"
	"syscall"
)

var errLeaseBusy = errors.New("physical device lease busy")

// physicalLease is an OS-owned advisory lock. Its release on process death is
// supplied by the kernel, unlike a timestamp or directory lock that could
// strand a real device after a broker crash.
type physicalLease struct{ file *os.File }

func acquireLease(path string) (*physicalLease, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
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
