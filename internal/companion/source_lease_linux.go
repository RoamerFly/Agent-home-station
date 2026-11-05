//go:build linux

package companion

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// isKnownProcPseudoHandle reports /proc/<pid>/fd targets that are conclusively
// not filesystem objects. Every other target must still be statted: a path
// that merely looks unrelated can be a hard link to the memory database.
func isKnownProcPseudoHandle(target string) bool {
	return strings.HasPrefix(target, "socket:") ||
		strings.HasPrefix(target, "pipe:") ||
		strings.HasPrefix(target, "anon_inode:")
}

// Legacy processes lack the sidecar lease. Check database handles owned by
// this Unix user before loading the maintenance snapshot. New processes are
// excluded atomically by the exclusive sidecar lease held by the caller.
func verifyLegacyMemoryOffline(path string) error {
	return verifyLegacyMemoryOfflineInProc(path, "/proc", os.Getuid())
}

// verifyLegacyMemoryOfflineInProc is split out so the fail-closed descriptor
// scan can be tested against a controlled procfs-shaped tree. procRoot must be
// a procfs root in production; callers must not use an untrusted location.
func verifyLegacyMemoryOfflineInProc(path, procRoot string, uid int) error {
	target, e := os.Stat(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	processes, e := os.ReadDir(procRoot)
	if e != nil {
		return errors.New("cannot verify offline memory handles: procfs unavailable")
	}
	for _, process := range processes {
		if _, e = strconv.Atoi(process.Name()); e != nil {
			continue
		}
		base := filepath.Join(procRoot, process.Name())
		info, e := os.Stat(base)
		if e != nil {
			if os.IsNotExist(e) {
				continue
			}
			return e
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != uid {
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
			fdEntry := filepath.Join(fdpath, fd.Name())
			targetName, e := os.Readlink(fdEntry)
			if e != nil {
				if os.IsNotExist(e) {
					continue
				}
				return fmt.Errorf("cannot inspect memory handle pid=%s fd=%s: %w", process.Name(), fd.Name(), e)
			}
			if isKnownProcPseudoHandle(targetName) {
				continue
			}
			open, e := os.Stat(fdEntry)
			if e != nil {
				if os.IsNotExist(e) {
					continue
				}
				return fmt.Errorf("cannot inspect memory handle pid=%s fd=%s: %w", process.Name(), fd.Name(), e)
			}
			if os.SameFile(target, open) {
				return errors.New("memory database is open in another process; stop all Web instances before offline repair")
			}
		}
	}
	return nil
}
