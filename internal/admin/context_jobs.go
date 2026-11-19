package admin

import (
	"context"
	"errors"
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
	defer m.compressionMu.Unlock()
	if m.compressionClosed {
		return contextCompressionView{}, errors.New("压缩服务正在关闭，请稍后再试")
	}
	if m.compressionJobs == nil {
		m.compressionJobs = map[string]*contextCompressionJob{}
	}
	if current := m.compressionJobs[scope.ID]; current != nil && (current.State == "queued" || current.State == "running") {
		return compressionJobView(current), companion.ErrCompressionInProgress
	}
	trace := "compression-" + randomToken()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	job := &contextCompressionJob{ScopeID: scope.ID, Trace: trace, State: "queued", Message: "已排队，等待压缩", StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), cancel: cancel}
	m.compressionJobs[scope.ID] = job
	m.compressionWG.Add(1)
	m.record(bot.Record{Event: "context_compression_started", Category: "tool", ThreadID: scope.Thread, MessageID: trace, Content: "手动压缩上下文已排队", Status: "queued", Origin: "manual"})
	go m.runContextCompression(ctx, scope, job)
	return compressionJobView(job), nil
}

func (m *Manager) updateCompressionJob(job *contextCompressionJob, update func(*contextCompressionJob)) {
	m.compressionMu.Lock()
	defer m.compressionMu.Unlock()
	if current := m.compressionJobs[job.ScopeID]; current != job {
		return
	}
	update(job)
	job.UpdatedAt = time.Now().UTC()
}

func (m *Manager) runContextCompression(ctx context.Context, scope companion.Scope, job *contextCompressionJob) {
	defer m.compressionWG.Done()
	defer job.cancel()
	m.updateCompressionJob(job, func(current *contextCompressionJob) {
		current.State = "running"
		current.Message = "正在调用模型并保存摘要"
	})
	cfg := m.runtimeConfig()
	provider, err := m.makeModel(cfg)
	changed := false
	if err == nil {
		var engine *companion.Engine
		engine, err = companion.New(cfg.sharedCompanion(), m.memory, provider, cfg.SystemPrompt)
		if err == nil {
			changed, err = engine.CompressContext(ctx, companion.Input{Channel: scope.Channel, Account: scope.Account, Thread: scope.Thread}, true)
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
}
