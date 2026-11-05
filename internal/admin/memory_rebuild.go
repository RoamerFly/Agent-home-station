package admin

import (
	"context"
	"errors"
	"net/http"
	"time"
	"wechat-bot/internal/companion"
)

// Explicit rebuilds run independently of WeChat readiness and never send. They
// are persisted before launch and included in drain/close lifecycle accounting.
func (m *Manager) startMemoryRebuild(id string) error {
	m.memoryRunnerMu.Lock()
	defer m.memoryRunnerMu.Unlock()
	if m.memoryRunnersClosed || m.draining() {
		return errors.New("正在停止或等待更新，请稍后再试")
	}
	if m.memoryRunners == nil {
		m.memoryRunners = map[string]context.CancelFunc{}
	}
	if _, ok := m.memoryRunners[id]; ok {
		return companion.ErrCompressionInProgress
	}
	if !m.Config().Companion.Permissions.Remember {
		return errors.New("记忆权限未开启")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.memoryRunners[id] = cancel
	m.memoryRunnerWG.Add(1)
	if e := m.memory.QueueRebuild(ctx, id); e != nil {
		cancel()
		delete(m.memoryRunners, id)
		m.memoryRunnerWG.Done()
		return e
	}
	go func() {
		defer m.memoryRunnerWG.Done()
		defer func() { cancel(); m.memoryRunnerMu.Lock(); delete(m.memoryRunners, id); m.memoryRunnerMu.Unlock() }()
		// Config() is the public, redacted view. Internal provider calls must
		// retain the stored credential without exposing it to the UI.
		c := m.runtimeConfig()
		provider, e := m.makeModel(c)
		if e != nil {
			m.memory.MemoryError(ctx, id, e)
			return
		}
		engine, e := companion.New(c.sharedCompanion(), m.memory, provider, c.SystemPrompt)
		if e != nil {
			m.memory.MemoryError(ctx, id, e)
			return
		}
		cfg, e := m.loadMemoryServices()
		if e != nil {
			m.memory.MemoryError(ctx, id, e)
			return
		}
		engine.Config.Memory = &cfg.Settings
		engine.MemoryAllowed = func() bool { return m.Config().Companion.Permissions.Remember }
		if cfg.Maintenance.Model != "" {
			engine.MemoryModel, e = m.memoryProvider(cfg)
			if e != nil {
				m.memory.MemoryError(ctx, id, e)
				return
			}
		}
		for ctx.Err() == nil {
			if !m.Config().Companion.Permissions.Remember {
				m.memory.MemoryError(ctx, id, errors.New("memory permission revoked"))
				return
			}
			complete, err := m.memory.IndexArchives(ctx, id, 20)
			if err == nil {
				err = engine.MaintainScope(ctx, id, true)
			}
			if errors.Is(err, companion.ErrCompressionInProgress) {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
					continue
				}
			}
			if err != nil {
				m.memory.MemoryError(ctx, id, err)
				return
			}
			n, err := m.memory.RebuildEmbeddings(ctx, id, cfg.Embedding, 32)
			if err != nil {
				m.memory.MemoryError(ctx, id, err)
				return
			}
			pending, err := m.memory.MaintenancePending(ctx, id)
			if err != nil {
				m.memory.MemoryError(ctx, id, err)
				return
			}
			if complete && !pending && n < 32 {
				_ = m.memory.CompleteRebuild(ctx, id)
				return
			}
		}
	}()
	return nil
}
func (m *Manager) memoryRebuildBusy() bool {
	m.memoryRunnerMu.Lock()
	defer m.memoryRunnerMu.Unlock()
	return len(m.memoryRunners) > 0
}
func (m *Manager) closeMemoryRunners() {
	m.memoryRunnerMu.Lock()
	m.memoryRunnersClosed = true
	for _, cancel := range m.memoryRunners {
		cancel()
	}
	m.memoryRunnerMu.Unlock()
	m.memoryRunnerWG.Wait()
}

func (s *Server) memoryRebuildRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/memory/{scope}/rebuild", s.protected(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("scope")
		if _, e := s.manager.memoryScope(id); e != nil {
			fail(w, 404, "会话不存在")
			return
		}
		if e := s.manager.startMemoryRebuild(id); e != nil {
			fail(w, 409, e.Error())
			return
		}
		respond(w, 202, map[string]any{"queued": true, "scope": id, "status_url": "/api/memory/" + id, "automatic_budget_applies": false})
	}))
}
