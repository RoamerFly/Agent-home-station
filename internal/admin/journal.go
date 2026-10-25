package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/linuxnative"
)

// runtimeConfig is private: unlike Config(), it must retain credentials during reload.
func (m *Manager) runtimeConfig() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneConfig(m.config)
}
func (m *Manager) loadJournal() error {
	data, err := os.ReadFile(filepath.Join(m.dir, "activity.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || len(data) > 8<<20 || json.Unmarshal(data, &m.journal) != nil || len(m.journal) > 1000 {
		return errors.New("消息活动日志不可读，未自动覆盖")
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	retained := make([]LogEntry, 0, len(m.journal))
	for _, entry := range m.journal {
		stamp, err := time.Parse(time.RFC3339Nano, entry.Time)
		if err != nil {
			return errors.New("消息日志时间格式无效")
		}
		if !stamp.Before(cutoff) {
			retained = append(retained, entry)
		}
	}
	if len(retained) != len(m.journal) {
		m.journal = retained
		data, _ := json.Marshal(retained)
		if err := writePrivate(filepath.Join(m.dir, "activity.json"), data); err != nil {
			return err
		}
	}
	return nil
}
func logText(s string, limit int) string {
	if !utf8.ValidString(s) {
		return "[编码无效]"
	}
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…[截断]"
}
func (m *Manager) record(r bot.Record) {
	if m.surfHistory != nil {
		if err := m.surfHistory.recordActivity(r); err != nil {
			r.Reason = "surf_history_persistence_failed"
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	entry := LogEntry{SchemaVersion: 2, ID: m.nextLogIDLocked(), Time: now.Format(time.RFC3339Nano), Event: r.Event, Category: r.Category, ThreadID: logText(r.ThreadID, 256), Sender: logText(r.Sender, 256), Content: logText(r.Content, 4096), Reason: logText(r.Reason, 512), Status: r.Status, MessageID: logText(r.MessageID, 512), Origin: r.Origin}
	entry.SourceKind = "private"
	if strings.HasPrefix(r.MessageID, "surf-") {
		entry.Origin = "surf"
	}
	entry.SendID = logText(r.SendID, 128)
	entry.CallID = logText(r.CallID, 128)
	entry.Tool = logText(r.Tool, 160)
	entry.ToolCacheHit = r.ToolCacheHit
	entry.ToolRequests = r.ToolRequests
	entry.ToolRetries = r.ToolRetries
	entry.ToolHTTPStatus = r.ToolHTTPStatus
	entry.ToolCode = logText(r.ToolCode, 64)
	entry.ToolStage = logText(r.ToolStage, 64)
	entry.TargetURL = logText(r.TargetURL, 2048)
	entry.Query = logText(r.Query, 512)
	entry.DurationMS = r.DurationMS
	entry.Bytes = r.Bytes
	entry.MediaCount = r.MediaCount
	entry.ModelHTTPStatus = r.ModelHTTPStatus
	entry.ModelAttempts = r.ModelAttempts
	entry.ModelMaxAttempts = r.ModelMaxAttempts
	entry.ModelStage = logText(r.ModelStage, 64)
	entry.ModelRetryDelayMS = r.ModelRetryDelayMS
	entry.Source = r.ThreadID
	if r.ThreadID == "" {
		entry.SourceKind = "system"
		entry.Source = "工具与系统"
	}
	if strings.HasSuffix(r.ThreadID, "@chatroom") {
		entry.SourceKind = "group"
		entry.Source = "群聊"
		for _, rule := range m.config.GroupRules {
			if rule.ID == r.ThreadID && rule.Name != "" {
				entry.Source = rule.Name
				break
			}
		}
	}
	if r.ThreadID == "filehelper" {
		entry.Source = "文件传输助手"
	}
	if m.config.APIKey != "" {
		entry.Content = strings.ReplaceAll(entry.Content, m.config.APIKey, "[已隐藏密钥]")
		entry.Reason = strings.ReplaceAll(entry.Reason, m.config.APIKey, "[已隐藏密钥]")
		entry.Query = strings.ReplaceAll(entry.Query, m.config.APIKey, "[已隐藏密钥]")
	}
	m.journal = append(m.journal, entry)
	cutoff := now.Add(-7 * 24 * time.Hour)
	first := 0
	for first < len(m.journal) {
		stamp, err := time.Parse(time.RFC3339Nano, m.journal[first].Time)
		if err != nil || !stamp.Before(cutoff) {
			break
		}
		first++
	}
	m.journal = m.journal[first:]
	if len(m.journal) > 1000 {
		m.journal = m.journal[len(m.journal)-1000:]
	}
	data, err := json.Marshal(m.journal)
	if err != nil || len(data) > 8<<20 || writePrivate(filepath.Join(m.dir, "activity.json"), data) != nil {
		m.status.LastEvent = "message_log_persistence_failed"
	}
}
func (m *Manager) Logs() []LogEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append(append([]LogEntry{}, m.logs...), m.journal...)
	for i := range out {
		if out[i].Category == "" {
			out[i].Category = eventCategory(out[i].Event)
		}
		if out[i].ID == "" {
			out[i].ID = fmt.Sprintf("system:%s:%s:%d", out[i].Time, out[i].Event, i)
		}
		if out[i].SourceKind == "" {
			out[i].SourceKind = "system"
			out[i].Source = "系统"
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, out[i].Time)
		b, _ := time.Parse(time.RFC3339Nano, out[j].Time)
		return a.Before(b)
	})
	return out
}

func sendCategory(status string) string {
	if status == "failed" || status == "unknown" || status == "needs_review" {
		return "error"
	}
	return "sent"
}
func (m *Manager) manualContent(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.journal) - 1; i >= 0; i-- {
		if m.journal[i].MessageID == key && m.journal[i].Content != "" {
			return m.journal[i].Content
		}
	}
	return ""
}
func (m *Manager) recordManualSend(input linuxnative.ToolSend, status string, reason string, sendID string) {
	content := input.Text
	if input.Kind != "text" {
		content = "[" + input.Kind + "] " + input.SourceID
		if source, ok := m.tools.source(input.SourceID, m.native.Account(), input.Kind); ok {
			content = "[" + input.Kind + "] " + source.Filename
		}
	}
	m.record(bot.Record{Event: "manual_send_submitted", Category: sendCategory(status), ThreadID: input.ThreadID, Content: content, Origin: "manual", Status: status, MessageID: input.IdempotencyKey, Reason: reason, SendID: sendID})
}

func eventCategory(event string) string {
	if strings.Contains(event, "failed") || strings.Contains(event, "error") || strings.Contains(event, "unknown") {
		return "error"
	}
	if strings.HasPrefix(event, "mcp_") || strings.HasPrefix(event, "plugin_") {
		return "tool"
	}
	if strings.HasPrefix(event, "character_") || strings.HasPrefix(event, "message_skipped") {
		return "decision"
	}
	return "system"
}

func (m *Manager) nextLogIDLocked() string {
	m.logSequence++
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), m.logSequence)
}
