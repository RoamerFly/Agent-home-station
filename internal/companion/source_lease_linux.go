//go:build linux

package companion

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// Legacy processes lack the sidecar lease. Check database handles owned by
// this Unix user before loading the maintenance snapshot. New processes are
// excluded atomically by the exclusive sidecar lease held by the caller.
func verifyLegacyMemoryOffline(path string) error {
	target, e := os.Stat(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	processes, e := os.ReadDir("/proc")
	if e != nil {
		return errors.New("cannot verify offline memory handles: procfs unavailable")
	}
	for _, process := range processes {
		if _, e = strconv.Atoi(process.Name()); e != nil {
			continue
		}
		base := filepath.Join("/proc", process.Name())
		info, e := os.Stat(base)
		if e != nil {
			if os.IsNotExist(e) {
				continue
			}
			return e
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != os.Getuid() {
			continue
		}
		fdpath := filepath.Join(base, "fd")
		fds, e := os.ReadDir(fdpath)
		if e != nil {
			if os.IsNotExist(e) {
				continue
			}
			return errors.New("cannot verify offline memory handles for this user")
		}
		for _, fd := range fds {
			open, e := os.Stat(filepath.Join(fdpath, fd.Name()))
			if e != nil {
				if os.IsNotExist(e) {
					continue
				}
				return errors.New("cannot inspect an open memory handle")
			}
			if os.SameFile(target, open) {
				return errors.New("memory database is open in another process; stop all Web instances before offline repair")
			}
		}
	}
	return nil
}
