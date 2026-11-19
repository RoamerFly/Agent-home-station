// Package hostfile provides durable private host state primitives.
package hostfile

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Windows FlushFileBuffers serializes access to the filesystem cache more
// reliably than the SQLite layer alone.  The test and desktop hosts can write
// several private records at once during startup and shutdown.  Serializing
// the durable flush prevents one handle from holding another flush open for
// minutes while retaining the same write-then-rename durability contract.
var privateSyncMu sync.Mutex

// WritePrivate replaces a private record through a synchronized sibling file.
func WritePrivate(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return errors.New("无法创建私有配置文件")
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		privateSyncMu.Lock()
		err = file.Sync()
		privateSyncMu.Unlock()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.New("无法写入配置文件")
	}
	if os.Rename(file.Name(), path) != nil {
		return errors.New("无法保存配置文件")
	}
	return nil
}
