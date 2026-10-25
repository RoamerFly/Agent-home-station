package admin

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/historyarchive"
)

func (m *Manager) historyArchive() historyarchive.Store {
	return historyarchive.Store{Root: filepath.Join(m.dir, "archive")}
}

// Activity belongs to this administrator's runtime directory. Legacy log rows
// have no reliable account field, so do not invent account ownership for them.
func activityScope() string { return companion.ScopeID("admin", "instance", "activity") }

func (m *Manager) pruneJournal(now time.Time) error {
	cutoff := now.Add(-7 * 24 * time.Hour)
	retained, dropped := []LogEntry{}, []LogEntry{}
	for _, entry := range m.journal {
		stamp, err := time.Parse(time.RFC3339Nano, entry.Time)
		if err != nil {
			return errors.New("消息日志时间格式无效")
		}
		if stamp.Before(cutoff) {
			dropped = append(dropped, entry)
		} else {
			retained = append(retained, entry)
		}
	}
	if len(retained) > 1000 {
		// Rotate a small block rather than create one archive file per new log
		// row. The active window remains bounded at 873–1000 recent rows.
		split := max(len(retained)-1000, 128)
		dropped = append(dropped, retained[:split]...)
		retained = retained[split:]
	}
	if len(dropped) == 0 {
		return nil
	}
	for start := 0; start < len(dropped); start += 128 {
		if _, err := m.historyArchive().Put("activity", activityScope(), dropped[start:min(start+128, len(dropped))]); err != nil {
			return errors.New("日志归档失败，原活跃记录保留")
		}
	}
	b, err := json.Marshal(retained)
	if err != nil {
		return err
	}
	if err = writePrivate(filepath.Join(m.dir, "activity.json"), b); err != nil {
		return err
	}
	m.journal = retained
	return nil
}

func (m *Manager) archiveLogs(id string) ([]LogEntry, error) {
	batch, _, err := m.historyArchive().Read("activity", activityScope(), id)
	if err != nil {
		return nil, err
	}
	var items []LogEntry
	if json.Unmarshal(batch.Data, &items) != nil || len(items) > 128 {
		return nil, errors.New("日志归档内容无效")
	}
	return items, nil
}
