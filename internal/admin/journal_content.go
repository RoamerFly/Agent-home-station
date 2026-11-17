package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Long execution bodies are content-addressed private data. The small journal
// and its archives retain references instead of silently discarding the tail.
func (m *Manager) storeLogContent(entry *LogEntry) error {
	if len(entry.Content) <= 4096 {
		return nil
	}
	sum := sha256.Sum256([]byte(entry.Content))
	id := hex.EncodeToString(sum[:])
	dir := filepath.Join(m.dir, "activity-content")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(dir, id+".txt"), []byte(entry.Content)); err != nil {
		return err
	}
	entry.ContentRef, entry.Content = id, ""
	return nil
}

func (m *Manager) restoreLogContent(entry *LogEntry) {
	if entry.ContentRef == "" {
		return
	}
	id, err := hex.DecodeString(entry.ContentRef)
	if err == nil && len(id) == sha256.Size {
		body, readErr := os.ReadFile(filepath.Join(m.dir, "activity-content", hex.EncodeToString(id)+".txt"))
		if readErr == nil && fmt.Sprintf("%x", sha256.Sum256(body)) == entry.ContentRef {
			entry.Content = string(body)
			return
		}
	}
	entry.Content = "[完整执行内容文件不可读，请检查本机活动记录文件]"
}
