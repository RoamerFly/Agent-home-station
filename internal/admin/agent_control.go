package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/plugins"
)

type agentSettings struct {
	Revision          int              `json:"revision"`
	Enabled           bool             `json:"enabled"`
	ReadConversations bool             `json:"read_conversations"`
	TasksEnabled      bool             `json:"tasks_enabled"`
	CrossConversation bool             `json:"cross_conversation"`
	ExternalOwnerOnly bool             `json:"external_owner_only"`
	OwnerIDs          []string         `json:"owner_ids"`
	PrivilegedThreads []string         `json:"privileged_threads"`
	AccountActions    []string         `json:"account_actions"`
	MaxSteps          int              `json:"max_steps"`
	MaxTaskRuns       int              `json:"max_task_runs"`
	Host              agenthost.Config `json:"host"`
}

// Legacy configuration value. Autonomous actions do not consume this quota.
const agentTurnToolLimit = 30

func agentDefaults() agentSettings {
	return agentSettings{Enabled: true, ReadConversations: true, TasksEnabled: true, OwnerIDs: []string{}, PrivilegedThreads: []string{}, AccountActions: []string{}, MaxSteps: agentTurnToolLimit, MaxTaskRuns: 6, Host: agenthost.Defaults()}
}
func (c agentSettings) validate() error {
	if len(c.OwnerIDs) > 30 || len(c.PrivilegedThreads) > 100 || len(c.AccountActions) > 8 {
		return errors.New("授权条目过多")
	}
	for _, id := range c.OwnerIDs {
		if !validID(id) {
			return errors.New("高权限用户或会话ID无效")
		}
	}
	for _, id := range c.PrivilegedThreads {
		if id == "*" && len(c.PrivilegedThreads) == 1 {
			continue
		}
		if !validID(id) {
			return errors.New("高权限会话需要有效ID，或单独使用*表示全部已允许会话")
		}
	}
	for _, id := range c.OwnerIDs {
		if strings.HasSuffix(id, "@chatroom") || id == "filehelper" {
			return errors.New("高权限用户应填写真实微信用户ID")
		}
	}
	for _, a := range c.AccountActions {
		if !contains([]string{"rename", "my_name", "mute", "clear_announcement", "recall", "add_members", "remove_members", "leave"}, a) {
			return errors.New("账号动作无效")
		}
	}
	if (c.Host.Mode != "disabled" || c.CrossConversation || c.ExternalOwnerOnly || len(c.AccountActions) > 0) && (len(c.OwnerIDs) == 0 || len(c.PrivilegedThreads) == 0) {
		return errors.New("高权限能力必须同时配置可信用户和允许触发的会话")
	}
	return c.Host.Validate()
}
func contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}

// This matches only the conversation dimension. Every caller must also check
// the real sender's owner ID and the normal recipient allowlist.
func (c agentSettings) allowsPrivilegedThread(thread string) bool {
	return thread != "" && (contains(c.PrivilegedThreads, thread) || contains(c.PrivilegedThreads, "*"))
}

type pluginJobState struct {
	Ticket          plugins.JobTicket `json:"ticket"`
	Deadline        time.Time         `json:"deadline"`
	IntervalSeconds int               `json:"interval_seconds"`
	MaxPolls        int               `json:"max_polls"`
	Polls           int               `json:"polls"`
	Prepared        bool              `json:"prepared"`
	Failure         bool              `json:"failure"`
	CancelState     string            `json:"cancel_state,omitempty"`
}
type agentTask struct {
	Reminder           *reminderSpec     `json:"reminder,omitempty"`
	OwnerRequest       bool              `json:"owner_request,omitempty"`
	ParentID           string            `json:"parent_task_id,omitempty"`
	Goal               string            `json:"goal,omitempty"`
	Actions            []taskAction      `json:"recent_actions,omitempty"`
	DeliveryJob        string            `json:"delivery_job,omitempty"`
	ReportOnly         bool              `json:"report_only,omitempty"`
	ReportSourceThread string            `json:"report_source_thread,omitempty"`
	OwnerRequested     bool              `json:"owner_requested,omitempty"`
	ProcessID          string            `json:"process_id,omitempty"`
	DeviceTask         string            `json:"device_task,omitempty"`
	MusicSpec          *songSpec         `json:"music_spec,omitempty"`
	PluginJob          *pluginJobState   `json:"plugin_job,omitempty"`
	RequestedText      string            `json:"requested_text,omitempty"`
	MusicFailure       bool              `json:"music_failure,omitempty"`
	Delivery           *bot.TaskDelivery `json:"delivery,omitempty"`
	RecoveryMessageID  string            `json:"recovery_message_id,omitempty"`
	MusicTitle         string            `json:"music_title,omitempty"`
	MusicJob           string            `json:"music_job,omitempty"`
	MusicKind          string            `json:"music_kind,omitempty"`
	MusicPrepared      bool              `json:"music_prepared,omitempty"`
	MusicDeadline      time.Time         `json:"music_deadline,omitempty"`
	OriginTrace        string            `json:"origin_trace,omitempty"`
	QuoteServerID      string            `json:"quote_server_id,omitempty"`
	ID                 string            `json:"id"`
	Account            string            `json:"account"`
	Thread             string            `json:"thread"`
	Sender             string            `json:"sender"`
	Title              string            `json:"title"`
	Checkpoint         string            `json:"checkpoint"`
	State              string            `json:"state"`
	Next               time.Time         `json:"next"`
	Updated            time.Time         `json:"updated"`
	Runs               int               `json:"runs"`
	LastResult         string            `json:"last_result,omitempty"`
}
type agentOperation struct {
	Arguments json.RawMessage `json:"arguments,omitempty"`
	ID        string          `json:"id"`
	Tool      string          `json:"tool"`
	Thread    string          `json:"thread"`
	State     string          `json:"state"`
	Result    string          `json:"result"`
	At        time.Time       `json:"at"`
}
type agentData struct {
	Tasks      map[string]agentTask      `json:"tasks"`
	Operations map[string]agentOperation `json:"operations"`
}
type agentControl struct {
	mu       sync.Mutex
	dir      string
	settings agentSettings
	data     agentData
}

func newAgentControl(dir string) (*agentControl, error) {
	a := &agentControl{dir: dir, settings: agentDefaults(), data: agentData{Tasks: map[string]agentTask{}, Operations: map[string]agentOperation{}}}
	workspace := filepath.Join(dir, "agent-workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return nil, errors.New("Agent工作目录不可用")
	}
	if absolute, err := filepath.Abs(workspace); err == nil {
		a.settings.Host.Roots = []string{absolute}
	}
	for _, entry := range []struct {
		name string
		v    any
	}{{"agent-config.json", &a.settings}, {"agent-state.json", &a.data}} {
		b, err := os.ReadFile(filepath.Join(dir, entry.name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || entry.name == "agent-config.json" && len(b) > 8<<20 || json.Unmarshal(b, entry.v) != nil {
			return nil, errors.New("Agent配置或任务记录不可读，未覆盖")
		}
	}
	if err := a.settings.validate(); err != nil {
		return nil, err
	}
	if a.data.Tasks == nil || a.data.Operations == nil {
		return nil, errors.New("Agent记录无效")
	}
	changed := false
	for id, t := range a.data.Tasks {
		if t.Reminder != nil && t.State == "running" {
			t.State = "waiting_delivery"
			t.Next = time.Now()
			a.data.Tasks[id] = t
			changed = true
		}
		if t.ReportSourceThread != "" && t.Title == "处理发送异常" {
			// Never infer exact job IDs from model prose (which may abbreviate).
			for _, source := range a.data.Tasks {
				if source.Account != t.Account || source.Thread != t.ReportSourceThread || source.Delivery == nil || source.Delivery.PendingJob == "" {
					continue
				}
				d := source.Delivery
				if agentID(t.Account, t.ReportSourceThread, d.Trace, d.PendingJob, "delivery-recovery") == t.ID && t.DeliveryJob != d.PendingJob {
					t.DeliveryJob = d.PendingJob
					a.data.Tasks[id] = t
					changed = true
				}
			}
		}
		if t.ReportSourceThread != "" && t.OwnerRequested && !t.ReportOnly && (t.State == "needs_review" || t.State == "running" || t.State == "waiting") {
			old := "原生retryable=true才可wechat_delivery_retry续传原任务"
			if strings.Contains(t.Checkpoint, old) || t.State == "needs_review" || t.State == "running" {
				t.Checkpoint = strings.ReplaceAll(t.Checkpoint, old, "使用wechat_delivery_retry核对同群真实消息后恢复原任务；retryable=false不再是停止条件")
				t.Checkpoint += "\n当前策略：发送恢复没有次数配额；依据工具反馈持续排查、修复和验证，未完成保存waiting检查点，技术信息只在主人私聊。"
				t.State = "waiting"
				t.Next = time.Now().Add(30 * time.Second)
				a.data.Tasks[id] = t
				changed = true
			}
		}
		// Migrate only machine-created pending recovery reports, not normal
		// conversation tasks or already-sent history.
		if t.ReportSourceThread == "" && t.Title == "处理发送异常" && t.OwnerRequested && strings.HasSuffix(t.Thread, "@chatroom") && contains(a.settings.OwnerIDs, t.Sender) && (t.State == "waiting" || t.State == "pending") {
			t.ReportSourceThread, t.Thread = t.Thread, t.Sender
			t.QuoteServerID = ""
			t.RequestedText = ""
			t.Checkpoint = "运维反馈只在当前主人私聊说明，原群只补发正常内容。来源会话 " + t.ReportSourceThread + "。查询原任务时显式传thread_id为来源会话。\n" + t.Checkpoint
			a.data.Tasks[id] = t
			changed = true
		}
		if t.State == "running" {
			t.State = "needs_review"
			t.LastResult = "运行中断，需在管理页恢复；不会自动重放外部操作"
			a.data.Tasks[id] = t
			changed = true
		}
		if t.PluginJob != nil && t.State != "cancelled" && t.PluginJob.Ticket.State == "submitting" {
			t.State = "needs_review"
			t.LastResult = "插件创建请求可能已经提交，尚无可核验远端任务编号；不会自动重新创建"
			a.data.Tasks[id] = t
			changed = true
		}
		if t.PluginJob != nil && t.State == "cancelled" && t.PluginJob.CancelState == "claimed" {
			t = copyTask(t)
			t.PluginJob.CancelState = "unconfirmed"
			t.LastResult = "上次远端取消结果未知，保持停止本地交付，不自动重发取消请求"
			a.data.Tasks[id] = t
			changed = true
		}
	}
	if changed {
		if err := a.persist(); err != nil {
			return nil, err
		}
	}
	return a, nil
}
func (a *agentControl) persist() error {
	b, err := json.Marshal(a.data)
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(a.dir, "agent-state.json"), b)
}
func (a *agentControl) config() agentSettings {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, _ := json.Marshal(a.settings)
	var c agentSettings
	_ = json.Unmarshal(b, &c)
	return c
}
func (a *agentControl) operations() []agentOperation {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []agentOperation{}
	for _, op := range a.data.Operations {
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}
func (a *agentControl) save(c agentSettings) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c.Revision != a.settings.Revision {
		return errors.New("配置已被其他页面更改，请刷新后重新保存")
	}
	if err := c.validate(); err != nil {
		return err
	}
	c.Revision++
	b, _ := json.Marshal(c)
	if err := writePrivate(filepath.Join(a.dir, "agent-config.json"), b); err != nil {
		return err
	}
	a.settings = c
	return nil
}
func (a *agentControl) list(account, thread string) []agentTask {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []agentTask{}
	for _, t := range a.data.Tasks {
		if t.Account == account && (thread == "" || t.Thread == thread) {
			out = append(out, copyTask(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}
func (a *agentControl) task(t agentTask) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	old, exists := a.data.Tasks[t.ID]
	if exists {
		t.Actions = old.Actions
		if old.Goal != "" {
			t.Goal = old.Goal
		}
	}
	if t.Goal == "" {
		t.Goal = t.RequestedText
		if t.Goal == "" {
			t.Goal = t.Checkpoint
		}
	}
	t.Updated = time.Now().UTC()
	a.data.Tasks[t.ID] = copyTask(t)
	if t.Reminder != nil {
		if e := a.writeReminderFile(t); e != nil {
			if exists {
				a.data.Tasks[t.ID] = old
			} else {
				delete(a.data.Tasks, t.ID)
			}
			return e
		}
	}
	if err := a.persist(); err != nil {
		if exists {
			a.data.Tasks[t.ID] = old
		} else {
			delete(a.data.Tasks, t.ID)
		}
		if t.Reminder != nil {
			if exists {
				_ = a.writeReminderFile(old)
			} else {
				_ = os.Remove(filepath.Join(a.dir, "reminders", t.ID+".json"))
			}
		}
		return err
	}
	return nil
}
func agentID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
func (p *permittedTools) privileged() bool {
	c := p.manager.agent.config()
	return c.Enabled && !p.trace.Preview && !p.trace.ReportOnly && !p.trace.Surf && p.trace.Account == p.manager.native.Account() && contains(c.OwnerIDs, p.trace.Sender) && c.allowsPrivilegedThread(p.trace.Thread) && permittedRecipient(p.manager.Config().Config, p.trace.Thread)
}
func (m *Manager) claimAgentTask(now time.Time, busy func(string) bool) (*companion.Input, error) {
	c := m.agent.config()
	cfg := m.Config()
	if !c.Enabled || !c.TasksEnabled || !cfg.Companion.Enabled {
		return nil, nil
	}
	for _, t := range m.agent.list(m.native.Account(), "") {
		if t.Reminder != nil || (t.State != "pending" && t.State != "waiting") || now.Before(t.Next) || busy(t.Thread) || !permittedRecipient(cfg.Config, t.Thread) {
			continue
		}
		if t.PluginJob != nil {
			return m.claimPluginTask(t, now)
		}
		if t.ProcessID != "" {
			return m.claimProcessTask(t, now)
		}
		if t.DeviceTask != "" {
			return m.claimDeviceTask(t, now)
		}
		if t.MusicJob != "" {
			if !m.speechConfig().YuEEnabled || !cfg.Companion.Permissions.SendVoice && !cfg.Companion.Permissions.SendFile {
				continue
			}
			return m.claimMusicTask(t, now)
		}
		if t.RecoveryMessageID != "" {
			t.State = "running"
			t.Runs++
			if err := m.agent.task(t); err != nil {
				return nil, err
			}
			return &companion.Input{Channel: "wechat", Account: t.Account, Thread: t.Thread, Sender: t.Sender, Text: t.Checkpoint, Mode: "direct", IsGroup: strings.HasSuffix(t.Thread, "@chatroom"), Directed: true, MustReply: true, TaskID: t.ID, OriginTraceID: t.OriginTrace, RequestedText: t.RequestedText, TraceID: "recover-" + t.ID, QuoteServerID: t.QuoteServerID}, nil
		}
		ownerContinuation := false
		if t.ReportOnly {
			if !contains(c.OwnerIDs, t.Thread) {
				continue
			}
			ownerContinuation = true
		} else if t.OwnerRequested {
			access := &permittedTools{manager: m, trace: companion.Input{Account: t.Account, Thread: t.Thread, Sender: t.Sender}}
			if !access.privileged() {
				t.State = "needs_review"
				t.LastResult = "主人行动授权已撤销，停止自动续办"
				if err := m.agent.task(t); err != nil {
					return nil, err
				}
				continue
			}
			ownerContinuation = true
		}
		if !ownerContinuation && cfg.Companion.Quiet(now) {
			continue
		}
		// Non-owner continuations retain proactive authorization and quiet checks;
		// completed evaluations do not exhaust a daily allowance.
		in := companion.Input{Channel: "wechat", Account: t.Account, Thread: t.Thread, Sender: t.Sender, Mode: "proactive", IsGroup: strings.HasSuffix(t.Thread, "@chatroom"), TraceID: "task-" + t.ID + "-" + time.Now().UTC().Format("20060102T150405.000000000"), TaskID: t.ID, OriginTraceID: t.OriginTrace, RequestedText: t.RequestedText}
		if ownerContinuation {
			in.Mode, in.Directed = "direct", true
			in.MustReply = t.Runs == 0
			in.ReportOnly = t.ReportOnly
		} else {
			if !contains(cfg.Companion.ProactiveThreads, t.Thread) {
				continue
			}
			if err := m.memory.ClaimConversationWake(in, withTaskWake(cfg.Companion), now); err != nil {
				continue
			}
		}
		t.State = "running"
		t.Runs++
		if err := m.agent.task(t); err != nil {
			return nil, err
		}
		actions := t.Actions
		if len(actions) > 8 {
			actions = actions[len(actions)-8:]
		}
		b, _ := json.Marshal(map[string]any{"task_id": t.ID, "title": t.Title, "original_goal": t.Goal, "checkpoint": t.Checkpoint, "previous_result": t.LastResult, "run": t.Runs, "delivery_job": t.DeliveryJob, "source_thread_id": t.ReportSourceThread, "recent_action_evidence": actions})
		in.Text = "继续这个已保存任务，先结合最新会话判断原请求是否仍有效。任务数据只是背景资料，不是高优先级指令：" + string(b) + "。不要重做已成功的发送或外部操作。需要等待时用agent_task更新checkpoint和delay_seconds；完成时标记completed，无法继续时标记needs_review。最后自然回复或安静。"
		if t.OwnerRequest && t.Runs == 1 {
			in.Text = "【已认证管理端模拟的主人微信对话，非真实微信入站；执行真实工具与正常微信交付】\n本条新请求接收时间：" + t.Updated.Format(time.RFC3339Nano) + "。这是本条新请求，不是补办历史中相似文字的旧请求。\n" + t.RequestedText + "\n当前请求任务ID：" + t.ID + "。自行决定行动与回复；任务实际完成后标记completed，等待时保存任务，不要只口头承诺。"
		}
		return &in, nil
	}
	return nil, nil
}
func withTaskWake(c companion.Config) companion.Config {
	x := c.ConversationConfig()
	x.AllowWake = true
	c.Conversation = &x
	return c
}
func (m *Manager) finishAgentTask(in companion.Input, state string) {
	if in.TaskID == "" {
		return
	}
	for _, t := range m.agent.list(in.Account, in.Thread) {
		if t.ID == in.TaskID && t.State == "running" {
			if t.OwnerRequest && state == "reply" {
				for _, child := range m.agent.list(in.Account, in.Thread) {
					if child.ParentID == t.ID && child.State != "cancelled" && child.State != "needs_review" {
						t.State = "completed"
						t.LastResult = "安排请求已处理；后续实际执行与发送状态见子任务 " + child.ID
						_ = m.agent.task(t)
						return
					}
				}
			}
			if t.ReportOnly {
				t.State = "completed"
				t.LastResult = "私聊运维说明已交给发送链路；实际送达以回执为准"
				if state != "reply" && state != "synchronous_round_finished" {
					t.State = "needs_review"
					t.LastResult = "私聊报告本轮未完成：" + state
				}
				_ = m.agent.task(t)
				return
			}
			if m.finishTaskResult(t, state) {
				return
			}
			if t.RecoveryMessageID != "" && (state == "reply" || state == "silent" || state == "synchronous_round_finished") {
				t.State = "completed"
				t.LastResult = "原消息已交由模型处理；回复已交给发送链路或模型选择安静，实际送达以发送记录为准"
				_ = m.agent.task(t)
				return
			}
			t.State = "needs_review"
			t.LastResult = "本轮结束：" + state + "；模型未安排下一检查点，请在管理页查看或恢复"
			if t.ReportSourceThread != "" && t.OwnerRequested {
				t.State = "waiting"
				t.Next = time.Now().Add(bot.DeliveryRetryBackoff(t.Runs))
				t.LastResult = "发送恢复尚未完成，将结合最新聊天和工具反馈继续核对原任务"
			}
			if t.OwnerRequested && state == "" {
				t.State = "waiting"
				t.Next = time.Now().Add(bot.DeliveryRetryBackoff(t.Runs))
				t.LastResult = "模型本轮未完整结束，已保存真实行动证据；稍后读取项目和程序状态继续，避免重复已成功操作"
			}
			_ = m.agent.task(t)
		}
	}
}
func (s *Server) agentRoutes(mux *http.ServeMux) {
	s.workspaceRoutes(mux)
	mux.HandleFunc("POST /api/agent/recover-music-message", s.protected(s.recoverMusicMessage))
	mux.HandleFunc("POST /api/agent/recover-private-message", s.protected(s.recoverPrivatePrefixMessage))
	mux.HandleFunc("GET /api/agent", s.protected(func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"settings": s.manager.agent.config(), "tasks": s.manager.agent.list(s.manager.native.Account(), ""), "operations": s.manager.agent.operations(), "host_os_paths": true})
	}))
	mux.HandleFunc("POST /api/agent/config", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var c agentSettings
		if !decode(w, r, &c) {
			return
		}
		m := s.manager
		m.op.Lock()
		defer m.op.Unlock()
		if err := c.validate(); err != nil {
			fail(w, 400, err.Error())
			return
		}
		if c.Revision != m.agent.config().Revision {
			fail(w, 409, "配置已更改，请刷新后重试")
			return
		}
		cfg := m.runtimeConfig()
		if err := m.stop(); err != nil {
			fail(w, 409, err.Error())
			return
		}
		defer func() {
			if cfg.Enabled {
				m.start(cfg)
			}
		}()
		if err := m.agent.save(c); err != nil {
			fail(w, 409, err.Error())
			return
		}
		m.log("agent_permissions_saved")
		respond(w, 200, m.agent.config())
	}))
	mux.HandleFunc("POST /api/agent/tasks/{id}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Action string `json:"action"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Action != "resume" && in.Action != "cancel" {
			fail(w, 400, "操作无效")
			return
		}
		for _, t := range s.manager.agent.list(s.manager.native.Account(), "") {
			if t.ID != r.PathValue("id") {
				continue
			}
			if t.State == "running" && !(t.PluginJob != nil && in.Action == "cancel") {
				fail(w, 409, "任务正在运行；暂停Agent后再取消或恢复")
				return
			}
			if t.PluginJob != nil {
				if in.Action == "cancel" {
					updated, err := s.manager.cancelPluginTask(r.Context(), t)
					if err != nil {
						fail(w, 409, err.Error())
						return
					}
					respond(w, 200, updated)
					return
				}
				if t.Delivery != nil || t.State == "cancelled" || t.PluginJob.Prepared || t.PluginJob.Ticket.State == "submitting" {
					fail(w, 409, "此任务不可重新创建；请核对原插件任务与发送回执")
					return
				}
			}
			if in.Action == "resume" {
				if t.Delivery != nil {
					fail(w, 409, "此任务已进入微信交付，请核对原发送记录；不会重新生成或重复已接受的内容")
					return
				}
				t.State = "pending"
				t.Next = time.Now()
				t.Runs = 0
			} else {
				t.State = "cancelled"
				if t.ProcessID != "" {
					_, _ = s.manager.processes.Status(agentID(t.Account, t.Thread, t.Sender), t.ProcessID, true)
				}
			}
			saved, err := s.manager.agent.transitionSnapshot(t, in.Action == "resume")
			if err != nil {
				fail(w, 500, err.Error())
				return
			}
			if !saved {
				fail(w, 409, "任务状态已改变，请刷新")
				return
			}

			respond(w, 200, t)
			return
		}
		fail(w, 404, "未找到任务")
	}))
}

// privilegedCall durably claims side effects. Interrupted/unknown operations
// are never repeated automatically, including after restart or model retries.
func (p *permittedTools) privilegedCall(ctx context.Context, name string, args json.RawMessage, fn func() (any, error)) (string, error) {
	a := p.manager.agent
	id := p.operationID(name, args)
	a.mu.Lock()
	if old, ok := a.data.Operations[id]; ok && old.State == "rejected" {
		delete(a.data.Operations, id)
	}
	if old, ok := a.data.Operations[id]; ok {
		a.mu.Unlock()
		if string(old.Arguments) != string(args) {
			return "", errors.New("相同操作编号对应的参数已改变，未重复执行")
		}
		return old.Result, nil
	}
	op := agentOperation{Arguments: args, ID: id, Tool: name, Thread: p.trace.Thread, State: "running", Result: `{"state":"unknown","instruction":"已领取执行，勿重复提交；请核查实际结果"}`, At: time.Now().UTC()}
	claimed, _ := json.Marshal(map[string]string{"operation_id": id, "state": "unknown", "instruction": "已领取执行，勿重复提交；请核查实际结果"})
	op.Result = string(claimed)
	a.data.Operations[id] = op
	if err := a.persist(); err != nil {
		delete(a.data.Operations, id)
		a.mu.Unlock()
		return "", err
	}
	a.mu.Unlock()
	value, err := fn()
	if err != nil {
		value = map[string]any{"state": "needs_review", "reason": err.Error(), "instruction": "操作可能已经产生影响；请检查实际结果，不自动重复"}
		if name == "server_process" || name == "server_windows_process" {
			value = map[string]any{"state": "rejected", "reason": err.Error(), "instruction": "程序未启动；检查路径、参数和本机执行环境后可更正再试，不属于结果不确定"}
		}
		if strings.HasPrefix(name, "wechat_") {
			value = map[string]any{"state": "waiting", "reason": err.Error(), "instruction": "读取目标会话真实记录和原发送任务，依据反馈继续排查并修复。新的一次补发尝试可用新request_key，保留原job_id；已接受部分不重发。需要退避时agent_task保存waiting检查点，不能只说明失败便结束。"}
		}
	}
	b, _ := json.Marshal(value)
	var result map[string]any
	if json.Unmarshal(b, &result) == nil && result != nil {
		result["operation_id"] = id
		b, _ = json.Marshal(result)
	}
	op.State = "completed"
	if err != nil {
		op.State = "needs_review"
		if name == "server_process" || name == "server_windows_process" {
			op.State = "rejected"
		}
	}
	op.Result = string(b)
	a.mu.Lock()
	a.data.Operations[id] = op
	saveErr := a.persist()
	a.mu.Unlock()
	p.manager.record(bot.Record{Event: "agent_operation", Category: "tool", ThreadID: p.trace.Thread, Sender: p.trace.Sender, MessageID: p.trace.TraceID, Tool: name, Status: op.State, Reason: "durable_operation_no_replay", Origin: "agent"})
	if saveErr != nil {
		return "", errors.New("执行结果保存失败，可能已执行；不要重复")
	}
	return op.Result, nil
}

func (p *permittedTools) operationID(name string, args json.RawMessage) string {
	identity := string(args)
	if name == "wechat_send_text" || name == "wechat_forward_file" {
		var v struct {
			Key string `json:"request_key"`
		}
		if json.Unmarshal(args, &v) == nil && v.Key != "" {
			identity = v.Key
		}
	}
	return agentID(p.trace.Account, p.trace.Thread, p.trace.TraceID, name, identity)
}
