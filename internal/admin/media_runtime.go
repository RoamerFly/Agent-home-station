package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

func (m *Manager) draining() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.drainTicket != "" }

// Only an updater actively polling its ticket may keep the worker drained.
// A dead/disconnected updater must never leave incoming messages paused.
func (m *Manager) renewDrainLocked() {
	m.drainExpires = time.Now().Add(30 * time.Second)
	if m.drainTimer != nil {
		m.drainTimer.Stop()
	}
	ticket := m.drainTicket
	m.drainTimer = time.AfterFunc(30*time.Second, func() { m.expireDrain(ticket, time.Now()) })
}
func (m *Manager) expireDrain(ticket string, now time.Time) {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	if ticket == "" || m.drainTicket != ticket || now.Before(m.drainExpires) {
		m.mu.Unlock()
		return
	}
	m.drainTicket = ""
	done := m.done
	c := cloneConfig(m.config)
	m.mu.Unlock()
	m.resumeDrainedWorker(done, c)
	m.log("update_drain_expired_resumed")
}
func (m *Manager) resumeDrainedWorker(done chan struct{}, c Config) {
	if done == nil {
		return
	}
	select {
	case <-done:
		if c.Enabled {
			m.start(c)
		}
	case <-time.After(3 * time.Second):
		m.mu.Lock()
		if m.done == done && m.drainTicket == "" && m.status.State == "draining" {
			m.status.State = "running"
		}
		m.mu.Unlock()
	}
}
func (m *Manager) takeMediaSeeds() []bot.MediaSeed {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Retain seeds until the worker has persisted them. Repeated adoption is
	// deduplicated by job ID and survives a restart during the handoff.
	if len(m.mediaSeeds) == 0 {
		b, e := os.ReadFile(filepath.Join(m.dir, "media-recovery.json"))
		if e == nil && len(b) < 1<<20 {
			_ = json.Unmarshal(b, &m.mediaSeeds)
		}
	}
	return append([]bot.MediaSeed(nil), m.mediaSeeds...)
}
func (s *Server) mediaRuntimeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/runtime/drain", s.protected(func(w http.ResponseWriter, r *http.Request) {
		m := s.manager
		m.mu.Lock()
		if m.drainTicket == "" {
			m.drainTicket = randomToken()
		}
		ticket := m.drainTicket
		m.renewDrainLocked()
		m.status.State = "draining"
		m.mu.Unlock()
		respond(w, 200, map[string]any{"ticket": ticket, "status": "draining"})
	}))
	mux.HandleFunc("GET /api/runtime/drain", s.protected(func(w http.ResponseWriter, r *http.Request) {
		m := s.manager
		m.mu.Lock()
		ticket, done := m.drainTicket, m.done
		if ticket == "" || r.URL.Query().Get("ticket") != ticket {
			m.mu.Unlock()
			fail(w, 409, "更新等待已失效，请重新准备")
			return
		}
		m.renewDrainLocked()
		m.mu.Unlock()
		ready := done == nil
		if done != nil {
			select {
			case <-done:
				ready = true
			default:
			}
		}
		respond(w, 200, map[string]any{"ticket": ticket, "ready": ready})
	}))
	mux.HandleFunc("POST /api/runtime/undrain", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Ticket string `json:"ticket"`
		}
		if !decode(w, r, &in) {
			return
		}
		m := s.manager
		m.op.Lock()
		defer m.op.Unlock()
		m.mu.Lock()
		if m.drainTicket == "" || m.drainTicket != in.Ticket {
			m.mu.Unlock()
			fail(w, 409, "等待已失效")
			return
		}
		m.drainTicket = ""
		if m.drainTimer != nil {
			m.drainTimer.Stop()
		}
		done := m.done
		c := cloneConfig(m.config)
		m.mu.Unlock()
		// The worker may be between observing drain and returning. Wait briefly
		// for its state transition before choosing whether a restart is needed.
		m.resumeDrainedWorker(done, c)
		respond(w, 200, map[string]bool{"resumed": true})
	}))
	mux.HandleFunc("POST /api/media/recover", s.protected(s.recoverMedia))
}
func (s *Server) recoverMedia(w http.ResponseWriter, r *http.Request) {
	var in struct {
		JobID string `json:"job_id"`
		Trace string `json:"trace"`
		Kind  string `json:"kind"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.JobID) > 128 || len(in.Trace) > 8192 || in.JobID == "" || in.Trace == "" {
		fail(w, 400, "需要原任务与接收消息编号")
		return
	}
	m := s.manager
	native, ok := m.native.(nativeTools)
	if !ok {
		fail(w, 501, "原生附件后台不可用")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	job, err := native.ToolSendStatus(ctx, in.JobID)
	if err != nil || job.SourceID == "" || !permittedRecipient(m.Config().Config, job.ThreadID) || !companion.MediaPermitted(m.Config().Companion.Permissions, in.Kind) {
		fail(w, 400, "原任务不存在、发送权限未开放或会话不在白名单")
		return
	}
	message, err := m.native.Message(ctx, in.Trace)
	if err != nil || message.ThreadID != job.ThreadID {
		fail(w, 400, "原任务与接收消息不属于同一会话")
		return
	}
	input := companion.Input{Channel: "wechat", Account: m.native.Account(), Thread: job.ThreadID, Text: message.Body, Sender: message.SenderID, Mode: "direct", Directed: true, IsGroup: strings.HasSuffix(job.ThreadID, "@chatroom")}
	asset, err := m.rememberLegacyAsset(input, in.Kind, job.SourceID)
	if err != nil {
		fail(w, 400, "原始素材未通过账号归属校验")
		return
	}
	seed := bot.MediaSeed{Trace: in.Trace, Input: input, SourceID: job.SourceID, JobID: job.ID, Media: companion.GeneratedMedia{ID: job.SourceID, Kind: in.Kind, Title: asset.Title, AssetID: asset.ID}}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.mediaSeeds) == 0 {
		b, e := os.ReadFile(filepath.Join(m.dir, "media-recovery.json"))
		if e == nil && len(b) < 1<<20 {
			_ = json.Unmarshal(b, &m.mediaSeeds)
		}
	}
	for _, old := range m.mediaSeeds {
		if old.JobID == seed.JobID {
			respond(w, 200, map[string]any{"adopted": true, "asset_id": old.Media.AssetID})
			return
		}
	}
	if len(m.mediaSeeds) >= 50 {
		fail(w, 409, "待核对任务已达上限")
		return
	}
	seeds := append(append([]bot.MediaSeed(nil), m.mediaSeeds...), seed)
	b, e := json.Marshal(seeds)
	if e == nil {
		e = writePrivate(filepath.Join(m.dir, "media-recovery.json"), b)
	}
	if e != nil {
		fail(w, 500, errors.New("恢复记录保存失败").Error())
		return
	}
	m.mediaSeeds = seeds
	respond(w, 200, map[string]any{"adopted": true, "asset_id": asset.ID, "state": job.State, "new_send_submitted": false})
}
