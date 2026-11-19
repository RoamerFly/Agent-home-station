package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

// contextCompressionJob is deliberately kept separate from the durable
// conversation memory. A manual compression request is an asynchronous host
// operation; losing the browser connection must not cancel the model call or
// make the UI claim that a partial summary was saved.
type contextCompressionJob struct {
	ScopeID   string
	Trace     string
	State     string
	Code      string
	Message   string
	Changed   bool
	StartedAt time.Time
	UpdatedAt time.Time
	cancel    context.CancelFunc
}

type contextCompressionView struct {
	ScopeID   string `json:"scope_id"`
	Trace     string `json:"trace"`
	State     string `json:"state"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Changed   bool   `json:"changed"`
	StartedAt string `json:"started_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// contextCompressionJobRecord is the durable part of a compression job. The
// cancel function intentionally never crosses a process boundary.
type contextCompressionJobRecord struct {
	ScopeID   string    `json:"scope_id"`
	Trace     string    `json:"trace"`
	State     string    `json:"state"`
	Code      string    `json:"code,omitempty"`
	Message   string    `json:"message,omitempty"`
	Changed   bool      `json:"changed"`
	StartedAt time.Time `json:"started_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

const contextCompressionJobsFile = "context-compression-jobs.json"

func (m *Manager) compressionJobsPath() string {
	return filepath.Join(m.dir, contextCompressionJobsFile)
}

func compressionJobRecord(job *contextCompressionJob) contextCompressionJobRecord {
	if job == nil {
		return contextCompressionJobRecord{}
	}
	return contextCompressionJobRecord{ScopeID: job.ScopeID, Trace: job.Trace, State: job.State, Code: job.Code, Message: job.Message, Changed: job.Changed, StartedAt: job.StartedAt, UpdatedAt: job.UpdatedAt}
}

// persistCompressionJobsLocked writes the small status catalogue atomically.
// The caller must hold compressionMu so a status endpoint cannot observe a
// different in-memory state from the one written to disk.
func (m *Manager) persistCompressionJobsLocked() error {
	rows := make(map[string]contextCompressionJobRecord, len(m.compressionJobs))
	for scopeID, job := range m.compressionJobs {
		if job != nil {
			rows[scopeID] = compressionJobRecord(job)
		}
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(m.compressionJobsPath(), data)
}

// loadCompressionJobs restores the last visible status after a process
// restart. A queued/running request cannot safely resume its model call, so it
// becomes a terminal failed job with the raw memory left untouched. This is
// preferable to reporting idle and making the UI silently lose the request.
func (m *Manager) loadCompressionJobs() error {
	data, err := os.ReadFile(m.compressionJobsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取压缩任务状态失败: %w", err)
	}
	if len(data) > 1<<20 {
		return errors.New("压缩任务状态文件过大")
	}
	var rows map[string]contextCompressionJobRecord
	if err := json.Unmarshal(data, &rows); err != nil {
		return fmt.Errorf("压缩任务状态文件无效: %w", err)
	}
	now := time.Now().UTC()
	m.compressionMu.Lock()
	m.compressionJobs = make(map[string]*contextCompressionJob, len(rows))
	recovered := false
	for key, row := range rows {
		scopeID := row.ScopeID
		if scopeID == "" {
			scopeID = key
		}
		if scopeID == "" {
			continue
		}
		if row.StartedAt.IsZero() {
			row.StartedAt = now
		}
		if row.UpdatedAt.IsZero() {
			row.UpdatedAt = row.StartedAt
		}
		if row.State == "queued" || row.State == "running" {
			row.State = "failed"
			row.Code = "context_compression_interrupted"
			row.Message = "工作站在压缩完成前重启，原始记忆保留；请重新压缩"
			row.UpdatedAt = now
			recovered = true
		}
		m.compressionJobs[scopeID] = &contextCompressionJob{ScopeID: scopeID, Trace: row.Trace, State: row.State, Code: row.Code, Message: row.Message, Changed: row.Changed, StartedAt: row.StartedAt, UpdatedAt: row.UpdatedAt}
	}
	if recovered {
		err = m.persistCompressionJobsLocked()
	}
	m.compressionMu.Unlock()
	if err != nil {
		return fmt.Errorf("保存重启后的压缩任务状态失败: %w", err)
	}
	return nil
}

func compressionJobView(job *contextCompressionJob) contextCompressionView {
	if job == nil {
		return contextCompressionView{State: "idle"}
	}
	view := contextCompressionView{
		ScopeID: job.ScopeID,
		Trace:   job.Trace,
		State:   job.State,
		Code:    job.Code,
		Message: job.Message,
		Changed: job.Changed,
	}
	if !job.StartedAt.IsZero() {
		view.StartedAt = job.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if !job.UpdatedAt.IsZero() {
		view.UpdatedAt = job.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return view
}

func (m *Manager) compressionStatus(scopeID string) contextCompressionView {
	m.compressionMu.Lock()
	defer m.compressionMu.Unlock()
	return compressionJobView(m.compressionJobs[scopeID])
}

func (m *Manager) enqueueContextCompression(scope companion.Scope) (contextCompressionView, error) {
	if scope.ID == "" {
		return contextCompressionView{}, errors.New("会话标识无效")
	}
	if m.draining() {
		return contextCompressionView{}, errors.New("正在停止或等待更新，请稍后再试")
	}
	m.compressionMu.Lock()
	if m.compressionClosed {
		m.compressionMu.Unlock()
		return contextCompressionView{}, errors.New("压缩服务正在关闭，请稍后再试")
	}
	if m.compressionJobs == nil {
		m.compressionJobs = map[string]*contextCompressionJob{}
	}
	if current := m.compressionJobs[scope.ID]; current != nil && (current.State == "queued" || current.State == "running") {
		m.compressionMu.Unlock()
		return compressionJobView(current), companion.ErrCompressionInProgress
	}
	trace := "compression-" + randomToken()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	job := &contextCompressionJob{ScopeID: scope.ID, Trace: trace, State: "queued", Message: "已排队，等待压缩", StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), cancel: cancel}
	m.compressionJobs[scope.ID] = job
	if err := m.persistCompressionJobsLocked(); err != nil {
		delete(m.compressionJobs, scope.ID)
		m.compressionMu.Unlock()
		cancel()
		return contextCompressionView{}, fmt.Errorf("压缩任务状态无法持久化: %w", err)
	}
	m.compressionWG.Add(1)
	view := compressionJobView(job)
	m.compressionMu.Unlock()
	m.record(bot.Record{Event: "context_compression_started", Category: "tool", ThreadID: scope.Thread, MessageID: trace, Content: "手动压缩上下文已排队", Status: "queued", Origin: "manual"})
	go m.runContextCompression(ctx, scope, job)
	return view, nil
}

func (m *Manager) updateCompressionJob(job *contextCompressionJob, update func(*contextCompressionJob)) {
	m.compressionMu.Lock()
	if current := m.compressionJobs[job.ScopeID]; current != job {
		m.compressionMu.Unlock()
		return
	}
	update(job)
	job.UpdatedAt = time.Now().UTC()
	err := m.persistCompressionJobsLocked()
	m.compressionMu.Unlock()
	if err != nil {
		m.log("context_compression_status_persistence_failed")
	}
}

func (m *Manager) runContextCompression(ctx context.Context, scope companion.Scope, job *contextCompressionJob) {
	defer m.compressionWG.Done()
	defer job.cancel()
	m.updateCompressionJob(job, func(current *contextCompressionJob) {
		current.State = "running"
		current.Message = "正在调用模型并保存摘要"
	})
	cfg := m.runtimeConfig()
	changed := false
	rememberEnabled := func() bool {
		return m.runtimeConfig().Companion.Permissions.Remember
	}
	var provider companion.LanguageModel
	var err error
	if !rememberEnabled() {
		err = companion.ErrCompressionPermissionRevoked
	} else {
		provider, err = m.makeModel(cfg)
	}
	if err == nil {
		var engine *companion.Engine
		engine, err = companion.New(cfg.sharedCompanion(), m.memory, provider, cfg.SystemPrompt)
		if err == nil {
			changed, err = engine.CompressContextWithGuard(ctx, companion.Input{Channel: scope.Channel, Account: scope.Account, Thread: scope.Thread}, true, rememberEnabled)
		}
	}
	if err != nil {
		code, message, _ := compressionFailure(err)
		m.updateCompressionJob(job, func(current *contextCompressionJob) {
			current.State = "failed"
			current.Code = code
			current.Message = message
		})
		record := bot.Record{Event: "context_compression_failed", Category: "error", ThreadID: scope.Thread, MessageID: job.Trace, Reason: code, Content: message, Status: "failed", Origin: "manual"}
		if diagnostic, ok := deepseek.ErrorDiagnostics(err); ok {
			record.ModelAttempts, record.ModelMaxAttempts, record.ModelHTTPStatus = diagnostic.Attempts, diagnostic.MaxAttempts, diagnostic.HTTPStatus
			record.ModelStage, record.DurationMS = diagnostic.Stage, diagnostic.DurationMS
		}
		m.record(record)
		return
	}
	m.updateCompressionJob(job, func(current *contextCompressionJob) {
		current.State = "completed"
		current.Changed = changed
		if changed {
			current.Message = "会话压缩处理完成"
		} else {
			current.Message = "近期消息不足，无需压缩"
		}
	})
	m.record(bot.Record{Event: "context_compression_completed", Category: "decision", ThreadID: scope.Thread, MessageID: job.Trace, Content: "会话压缩处理完成", Status: "completed", Origin: "manual"})
}

func (m *Manager) closeContextCompressions() {
	m.compressionMu.Lock()
	m.compressionClosed = true
	for _, job := range m.compressionJobs {
		if job != nil && (job.State == "queued" || job.State == "running") && job.cancel != nil {
			job.cancel()
		}
	}
	m.compressionMu.Unlock()
	m.compressionWG.Wait()
	m.compressionMu.Lock()
	if err := m.persistCompressionJobsLocked(); err != nil {
		m.compressionMu.Unlock()
		m.log("context_compression_status_persistence_failed")
		return
	}
	m.compressionMu.Unlock()
}
