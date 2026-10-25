package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wechat-bot/internal/bailian"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/sending"
)

type Native interface {
	bot.Source
	Ready() bool
	SendText(context.Context, sendingRequest) (sendingReceipt, error)
	Status(context.Context) (linuxnative.Status, error)
	Groups(context.Context, string) ([]linuxnative.Group, error)
	BeginLogin(context.Context, string) (linuxnative.LoginOperation, error)
	LoginStatus(context.Context, string) (linuxnative.LoginOperation, error)
	LoginQR(context.Context, string, int) ([]byte, error)
}

// Keep the public native client interface aligned with the shared sender.
// Aliases preserve its exact method signature for fake/native implementations.
type sendingRequest = sending.TextRequest
type sendingReceipt = sending.Receipt

type LogEntry struct {
	ToolCacheHit      bool   `json:"tool_cache_hit,omitempty"`
	ToolRequests      int    `json:"tool_requests,omitempty"`
	ToolRetries       int    `json:"tool_retries,omitempty"`
	ToolHTTPStatus    int    `json:"tool_http_status,omitempty"`
	ToolCode          string `json:"tool_code,omitempty"`
	ToolStage         string `json:"tool_stage,omitempty"`
	ModelHTTPStatus   int    `json:"model_http_status,omitempty"`
	ModelAttempts     int    `json:"model_attempts,omitempty"`
	ModelMaxAttempts  int    `json:"model_max_attempts,omitempty"`
	ModelStage        string `json:"model_stage,omitempty"`
	ModelRetryDelayMS int64  `json:"model_retry_delay_ms,omitempty"`
	CallID            string `json:"call_id,omitempty"`
	Tool              string `json:"tool,omitempty"`
	TargetURL         string `json:"target_url,omitempty"`
	Query             string `json:"query,omitempty"`
	DurationMS        int64  `json:"duration_ms,omitempty"`
	Bytes             int64  `json:"bytes,omitempty"`
	MediaCount        int    `json:"media_count,omitempty"`
	SendID            string `json:"send_id,omitempty"`
	SchemaVersion     int    `json:"schema_version,omitempty"`
	ID                string `json:"id,omitempty"`
	Time              string `json:"time"`
	Event             string `json:"event"`
	Category          string `json:"category,omitempty"`
	ThreadID          string `json:"thread_id,omitempty"`
	SourceKind        string `json:"source_kind,omitempty"`
	Source            string `json:"source,omitempty"`
	Sender            string `json:"sender,omitempty"`
	Content           string `json:"content,omitempty"`
	Reason            string `json:"reason,omitempty"`
	Status            string `json:"status,omitempty"`
	MessageID         string `json:"message_id,omitempty"`
	Origin            string `json:"origin,omitempty"`
}

type BotStatus struct {
	State     string `json:"state"`
	LastEvent string `json:"last_event"`
}
type Manager struct {
	speech      bailian.Config
	agent       *agentControl
	surfHistory *surfHistoryStore
	billing     *billingStore
	assetMu     sync.Mutex
	drainTicket string
	mediaSeeds  []bot.MediaSeed
	surf        surfState
	dir         string
	stateDir    string
	native      Native
	op          sync.Mutex
	mu          sync.Mutex
	config      Config
	status      BotStatus
	logs        []LogEntry
	journal     []LogEntry
	logSequence uint64
	cancel      context.CancelFunc
	done        chan struct{}
	memory      *companion.Store
	tools       *toolStore
	actions     *actionLedger
	mcp         mcpSettings
	plugins     *pluginLedger
}

func NewManager(dir, stateDir string, native Native) (*Manager, error) {
	if native == nil || dir == "" {
		return nil, errors.New("missing admin runtime configuration")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("管理目录不可用")
	}
	c, err := loadConfig(dir)
	if err != nil {
		return nil, err
	}
	memory, err := companion.NewStore(filepath.Join(dir, "companion"))
	if err != nil {
		return nil, err
	}
	tools, err := newToolStore(dir)
	if err != nil {
		return nil, err
	}
	actions, err := newActionLedger(dir)
	if err != nil {
		return nil, err
	}
	mcp, err := loadMCP(dir)
	if err != nil {
		return nil, err
	}
	plug, err := newPluginLedger(dir)
	if err != nil {
		return nil, err
	}
	if stateDir == "" {
		stateDir = filepath.Join(dir, "bot-state")
	}
	billing, err := newBillingStore(dir)
	if err != nil {
		return nil, err
	}
	surf, surfErr := loadSurf(dir)
	if surfErr != nil {
		return nil, surfErr
	}
	speech, err := loadSpeech(dir)
	if err != nil {
		return nil, err
	}
	agent, err := newAgentControl(dir)
	if err != nil {
		return nil, err
	}
	m := &Manager{speech: speech, agent: agent, dir: dir, stateDir: stateDir, native: native, config: c, status: BotStatus{State: "disabled"}, logs: []LogEntry{}, memory: memory, tools: tools, actions: actions, mcp: mcp, plugins: plug}
	m.surf = surf
	if assets, err := m.loadAssets(); err == nil {
		sources := map[string]map[string]string{}
		for _, a := range assets {
			if a.Account == native.Account() && a.URL != "" {
				if sources[a.Scope] == nil {
					sources[a.Scope] = map[string]string{}
				}
				sources[a.Scope][a.ID] = a.URL
			}
		}
		if err = memory.BackfillMediaSources(sources); err != nil {
			return nil, err
		}
	}
	m.surfHistory, err = newSurfHistory(dir)
	if err != nil {
		return nil, err
	}
	m.billing = billing
	if data, readErr := os.ReadFile(filepath.Join(dir, "audit.json")); readErr == nil {
		if len(data) > 128<<10 || json.Unmarshal(data, &m.logs) != nil || len(m.logs) > 100 {
			return nil, errors.New("运行审计记录无效，未自动覆盖")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, errors.New("运行审计记录不可读")
	}
	for i := range m.logs {
		if m.logs[i].ID == "" {
			m.logs[i].ID = m.nextLogIDLocked()
		}
	}
	if err := m.loadJournal(); err != nil {
		return nil, err
	}
	if err := m.surfHistory.importAccepted(m.journal); err != nil {
		return nil, err
	}
	if c.Enabled {
		m.start(c)
	}
	return m, nil
}
func (m *Manager) Config() ConfigView {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneConfig(m.config).view()
}
func cloneConfig(c Config) Config {
	if c.Companion.Context != nil {
		copy := *c.Companion.Context
		c.Companion.Context = &copy
	}
	if c.Companion.Conversation != nil {
		copy := *c.Companion.Conversation
		c.Companion.Conversation = &copy
	}
	c.DirectIDs = append([]string{}, c.DirectIDs...)
	c.GroupRules = append([]GroupRule{}, c.GroupRules...)
	c.Companion.ProactiveThreads = append([]string{}, c.Companion.ProactiveThreads...)
	c.Companion.ProactiveRules = append([]companion.ProactiveRule{}, c.Companion.ProactiveRules...)
	return c
}
func (m *Manager) log(event string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.LastEvent = event
	m.logs = append(m.logs, LogEntry{ID: m.nextLogIDLocked(), Time: time.Now().UTC().Format(time.RFC3339Nano), Event: event, Category: eventCategory(event)})
	if len(m.logs) > 100 {
		m.logs = m.logs[len(m.logs)-100:]
	}
	data, err := json.Marshal(m.logs)
	if err != nil || writePrivate(filepath.Join(m.dir, "audit.json"), data) != nil {
		m.status.LastEvent = "audit_persistence_failed"
	}
}

func (m *Manager) BotStatus() BotStatus { m.mu.Lock(); defer m.mu.Unlock(); return m.status }

func (m *Manager) Save(input ConfigInput) (ConfigView, error) {
	m.op.Lock()
	defer m.op.Unlock()
	return m.saveLocked(input)
}
func (m *Manager) saveLocked(input ConfigInput) (ConfigView, error) {
	m.mu.Lock()
	previous := cloneConfig(m.config)
	m.mu.Unlock()
	next := cloneConfig(input.Config)
	if next.Companion.Name == "" {
		next.Companion = previous.Companion
	}
	next.Companion = companion.Normalize(next.Companion)
	if !input.ClearKey && next.APIKey == "" && previous.APIKey != "" {
		oldURL, oldErr := url.Parse(previous.BaseURL)
		newURL, newErr := url.Parse(next.BaseURL)
		if oldErr == nil && newErr == nil && (oldURL.Scheme != newURL.Scheme || !strings.EqualFold(oldURL.Host, newURL.Host)) {
			return ConfigView{}, errors.New("更换服务商地址时，请重新填入对应的 API Key，防止旧密钥发往其他服务商")
		}
	}
	if input.ClearKey {
		next.APIKey = ""
	} else if next.APIKey == "" {
		next.APIKey = previous.APIKey
	}
	if err := validate(next); err != nil {
		return ConfigView{}, err
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return ConfigView{}, errors.New("配置编码失败")
	}
	// Stop/cancel old work before changing its role/rules. Interrupted sends
	// remain claimed in the same checkpoint and will not be replayed.
	if err = m.stop(); err != nil {
		return ConfigView{}, err
	}
	if err = writePrivate(filepath.Join(m.dir, "config.json"), data); err != nil {
		if previous.Enabled {
			m.start(previous)
		}
		return ConfigView{}, err
	}
	if next.APIKey == "" {
		_ = os.Remove(filepath.Join(m.dir, "provider-key"))
	}
	m.mu.Lock()
	m.config = next
	m.mu.Unlock()
	m.log("configuration_saved")
	if next.Enabled {
		m.start(next)
	}
	return next.view(), nil
}
func (m *Manager) Toggle(enabled bool) error {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	c := cloneConfig(m.config)
	m.mu.Unlock()
	c.Enabled = enabled
	_, err := m.saveLocked(ConfigInput{Config: c})
	return err
}
func (m *Manager) stop() error {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.drainTicket = ""
	if cancel != nil {
		m.status.State = "stopping"
		cancel()
	}
	m.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			return errors.New("机器人仍在停止，请稍后再试")
		}
	}
	m.mu.Lock()
	m.cancel = nil
	m.done = nil
	m.status.State = "disabled"
	m.mu.Unlock()
	return nil
}
func (m *Manager) Close() error          { m.op.Lock(); defer m.op.Unlock(); return m.stop() }
func (m *Manager) setState(state string) { m.mu.Lock(); m.status.State = state; m.mu.Unlock() }
func (m *Manager) makeModel(c Config) (*deepseek.Client, error) {
	if c.APIKey == "" {
		return nil, errors.New("请先保存 API Key")
	}
	keyFile := filepath.Join(m.dir, "provider-key")
	if err := writePrivate(keyFile, []byte(c.APIKey)); err != nil {
		return nil, err
	}
	return deepseek.New(deepseek.Config{BaseURL: c.BaseURL, KeyFile: keyFile, Model: c.Model, SystemPrompt: c.SystemPrompt, MaxTokens: c.MaxTokens, Compatible: c.Provider == "openai-compatible", OnUsage: func(u deepseek.Usage) {
		if m.billing != nil {
			if err := m.billing.record(c, u); err != nil {
				m.log("usage_record_save_failed")
			}
		}
	}})
}
func (m *Manager) start(c Config) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.mu.Lock()
	m.cancel = cancel
	m.drainTicket = ""
	m.done = done
	m.status.State = "waiting_login"
	m.mu.Unlock()
	go func() {
		defer close(done)
		for !m.native.Ready() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
		if ctx.Err() != nil {
			return
		}
		provider, err := m.makeModel(c)
		if err != nil {
			m.setState("error")
			m.log("provider_configuration_error")
			return
		}
		var model bot.Model = provider
		if c.Companion.Enabled {
			character, err := companion.New(c.Companion, m.memory, provider, c.SystemPrompt)
			if err != nil {
				m.setState("error")
				m.log("character_configuration_error")
				return
			}
			character.Images = m.tools.imageAssets(m.native.Account())
			if names, ok := m.native.(companion.NameLookup); ok {
				character.Names = names
			}
			access, maxCalls, toolErr := m.toolAccess()
			if toolErr != nil {
				m.log("mcp_configuration_error")
			} else if access != nil {
				character.Tools = access
				character.MaxToolCalls = maxCalls
				character.ToolLog = m.log
			}
			model = character
		}
		rules := map[string]bot.GroupRule{}
		for _, r := range c.GroupRules {
			rules[r.ID] = bot.GroupRule{Mode: r.Mode, Prefix: r.Prefix, Persona: r.Persona, ForceReplyWhenMentioned: r.ForceReplyWhenMentioned}
		}
		worker, err := bot.New(bot.Config{AgentTask: m.claimAgentTask, AgentTaskDone: m.finishAgentTask, AgentTaskDelivery: m.updateAgentDelivery, Draining: m.draining, MediaSeeds: m.takeMediaSeeds, StateDir: m.stateDir, SurfTask: m.claimSurf, SurfAllowed: m.surfAllowed, Options: &c.Behavior, Analyzer: mediaUnderstanding{manager: m, provider: provider}, AllowedThreads: c.allowed(), Prefix: c.Prefix, Cooldown: time.Duration(c.CooldownSeconds) * time.Second, MaxAge: time.Duration(c.MaxAgeSeconds) * time.Second, GroupRules: rules, Log: m.log, Record: m.record}, m.native, model, m.native)
		if err != nil {
			m.setState("error")
			m.log("bot_configuration_error")
			return
		}
		m.setState("running")
		m.log("bot_starting")
		err = worker.Run(ctx)
		if err == nil && ctx.Err() == nil && m.draining() {
			m.setState("drained")
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			m.setState("error")
			m.log("bot_stopped_check_connection_or_checkpoint")
		}
	}()
}
func (m *Manager) TestModel(ctx context.Context) error {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	c := cloneConfig(m.config)
	m.mu.Unlock()
	model, err := m.makeModel(c)
	if err != nil {
		return err
	}
	_, err = model.Complete(ctx, "请只回复：连接成功。")
	if err != nil {
		m.log("model_test_failed")
		d, _ := deepseek.ErrorDiagnostics(err)
		m.record(bot.Record{Event: "provider_test_failed", Category: "error", Origin: "manual_model", Reason: d.Code, Status: "failed", ModelHTTPStatus: d.HTTPStatus, ModelAttempts: d.Attempts, ModelMaxAttempts: d.MaxAttempts, ModelStage: d.Stage, DurationMS: d.DurationMS})
		return errors.New(deepseek.UserMessage(err))
	}
	m.log("model_test_succeeded")
	return nil
}
