package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"wechat-bot/internal/companion"
)

// Recovery reads the actual incoming message, preserving its recipient and scope.
// A deterministic task and the existing effect ledger prevent double generation.
func (s *Server) recoverMusicMessage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MessageID         string `json:"message_id"`
		PreparedJob       string `json:"prepared_job_id"`
		RedeliverPrepared bool   `json:"redeliver_prepared"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.RedeliverPrepared && in.PreparedJob == "" {
		fail(w, 400, "重新交付必须指定已准备完成的新歌曲任务")
		return
	}
	m := s.manager
	m.op.Lock()
	defer m.op.Unlock()
	msg, err := m.native.Message(r.Context(), in.MessageID)
	if err != nil || msg.ID != in.MessageID || msg.Direction != "incoming" || !permittedRecipient(m.Config().Config, msg.ThreadID) {
		fail(w, 400, "未找到已授权会话中的接收消息")
		return
	}
	at, err := time.Parse(time.RFC3339Nano, msg.CreatedAt)
	if err != nil || time.Since(at) > 24*time.Hour || at.After(time.Now().Add(time.Minute)) || !strings.Contains(msg.Body, "歌") && !strings.Contains(msg.Body, "唱") {
		fail(w, 400, "仅支持24小时内的歌曲请求")
		return
	}
	ac := m.agent.config()
	if !ac.Enabled || !ac.TasksEnabled || !m.localMusicPluginConfigured(r.Context()) {
		fail(w, 409, "请先开启Agent持续任务和本地歌曲生成")
		return
	}
	id := agentID(m.native.Account(), msg.ThreadID, msg.ID, "manual-music-recovery")
	// Explicit administrator redelivery adopts ONE newly prepared take. The
	// old delivery remains immutable; the new job is the idempotency identity.
	if in.RedeliverPrepared {
		id = agentID(id, in.PreparedJob)
	}
	for _, t := range m.agent.list(m.native.Account(), msg.ThreadID) {
		if t.ID == id || !in.RedeliverPrepared && t.OriginTrace == msg.ID && t.MusicJob != "" {
			respond(w, 200, t)
			return
		}
	}
	m.agent.mu.Lock()
	exists := false
	for _, op := range m.agent.data.Operations {
		if op.Thread != msg.ThreadID || op.Tool != "music_local_start" {
			continue
		}
		var result map[string]any
		_ = json.Unmarshal([]byte(op.Result), &result)
		if op.ID == agentID(m.native.Account(), msg.ThreadID, msg.ID, op.Tool, string(op.Arguments)) {
			exists = true
			break
		}
	}
	m.agent.mu.Unlock()
	if exists && !in.RedeliverPrepared {
		fail(w, 409, "原消息已有生成操作记录，请检查原任务，未重复生成")
		return
	}
	t := agentTask{ID: id, Account: m.native.Account(), Thread: msg.ThreadID, Sender: msg.SenderID, Title: "补办歌曲请求", RequestedText: msg.Body, Checkpoint: msg.Body, State: "pending", Next: time.Now(), OriginTrace: msg.ID, QuoteServerID: msg.ServerID, RecoveryMessageID: msg.ID}
	if in.PreparedJob != "" {
		if !validID(in.PreparedJob) || !m.Config().Companion.Permissions.SendVoice {
			fail(w, 400, "准备任务编号无效或未允许语音")
			return
		}
		for _, old := range m.agent.list(m.native.Account(), "") {
			if old.MusicJob == in.PreparedJob {
				fail(w, 409, "该歌曲已关联其他交付任务，请检查原任务，未重复安排")
				return
			}
		}
		p := &permittedTools{manager: m, trace: companion.Input{Channel: "wechat", Account: t.Account, Thread: t.Thread, Sender: t.Sender, TraceID: t.OriginTrace}}
		ctx := withLocalMusicRecovery(r.Context(), t, in.PreparedJob)
		b, _, err := p.yueRequest(ctx, "GET", "/jobs/"+in.PreparedJob, nil, "")
		var job struct {
			ID            string `json:"id"`
			State         string `json:"state"`
			ReferenceUsed bool   `json:"reference_used"`
			Recipe        struct {
				Title      string `json:"title"`
				SourceHash string `json:"reference_source_sha256"`
				songSpec
			} `json:"recipe"`
		}
		if err != nil || json.Unmarshal(b, &job) != nil || job.ID != in.PreparedJob || job.State != "completed" || songTitle(job.Recipe.Title) == "" || job.Recipe.Lyrics == "" && job.Recipe.Engine != "rvc" || (job.Recipe.Mode != "original" && job.Recipe.Mode != "cover") || job.Recipe.Mode == "cover" && (!job.ReferenceUsed || len(job.Recipe.ReferenceHash) != 64) {
			fail(w, 409, "需要已完成且保存真实配方的YuE或RVC歌曲；不会把未知文件当翻唱")
			return
		}
		t.MusicJob = in.PreparedJob
		t.MusicTitle = songTitle(job.Recipe.Title)
		t.MusicKind = "voice"
		t.MusicSpec = &job.Recipe.songSpec
		if len(job.Recipe.SourceHash) == 64 {
			t.MusicSpec.ReferenceHash = job.Recipe.SourceHash
		}
		t.MusicDeadline = time.Now().Add(30 * time.Minute)
		t.State = "waiting"
		t.Checkpoint = "管理员已准备完成歌曲，补发原请求；只查询并交付，不重新生成或转换"
		t.Title = "补发歌曲 · " + t.MusicTitle
	}
	if err := m.agent.task(t); err != nil {
		fail(w, 500, err.Error())
		return
	}
	respond(w, 201, t)
}
