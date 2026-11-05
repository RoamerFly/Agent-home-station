package companion

import (
	"errors"
	"os"
	"path/filepath"
)

// NewOfflineStore acquires the same cross-process lease used by live stores,
// exclusively. No scope snapshot can be loaded before offline admission.
func NewOfflineStore(dir string) (*Store, error) { return openStoreWithLease(dir, true) }
func openStoreWithLease(dir string, exclusive bool) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, ".memory-access.lock")
	if info, err := os.Lstat(lockPath); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, errors.New("unsafe memory access lease")
	}
	lease, err := lockMemoryAccess(lockPath, exclusive)
	if err != nil {
		return nil, err
	}
	if exclusive {
		if err = verifyLegacyMemoryOffline(filepath.Join(dir, "memory.sqlite")); err != nil {
			lease.Close()
			return nil, err
		}
	}
	store, err := openSQLiteStoreUnlocked(dir)
	if err != nil {
		lease.Close()
		return nil, err
	}
	store.accessLease = lease
	return store, nil
}
