package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

// LocalHomeHandler owns the local chat and this station's app workers.
type LocalHomeHandler struct {
	http.Handler
	chat   *localChatService
	home   *localHomeService
	wechat *stationWechat
}

// BeginShutdown is deliberately separate from Close.  It first makes the
// station unavailable to new conversations and cancels an in-flight model
// request, so HTTP shutdown does not spend its grace period waiting for a
// model/tool call that can no longer produce a usable result.
func (h *LocalHomeHandler) BeginShutdown() {
	if h != nil && h.chat != nil {
		h.chat.beginShutdown()
		if h.wechat != nil {
			h.wechat.beginShutdown()
		}
	}
}

func (h *LocalHomeHandler) Close() error {
	return h.CloseContext(context.Background())
}

// CloseContext is the bounded counterpart used by the desktop lifecycle.
// BeginShutdown is always applied first; if an uncooperative model or runner
// still owns the turn, the caller regains control at its explicit deadline and
// a later CloseContext can finish the same close without replaying anything.
func (h *LocalHomeHandler) CloseContext(ctx context.Context) error {
	if h == nil || h.chat == nil {
		return nil
	}
	h.BeginShutdown()
	err := h.chat.closeContext(ctx)
	if h.wechat != nil {
		err = errors.Join(err, h.wechat.closeContext(ctx))
	}
	return err
}

func (h *LocalHomeHandler) runtime(w http.ResponseWriter, _ *http.Request) {
	if h == nil || h.home == nil {
		fail(w, http.StatusServiceUnavailable, "本机成长站不可用")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, http.StatusOK, map[string]any{"kind": "local-station", "pid": os.Getpid(), "data_directory": h.home.dataDir})
}

type localChatService struct {
	home      *localHomeService
	models    *localModelStore
	mu        sync.Mutex
	turn      sync.Mutex
	busy      atomic.Bool
	store     *companion.Store
	fishID    string
	ownerID   string
	closed    bool
	stopping  atomic.Bool
	closeOnce sync.Once
	closeErr  error
	ctx       context.Context
	cancel    context.CancelFunc
	windows   *agenthost.WindowsConfig
	processes *localProcessTools
	wechat    *stationWechat
	// Tests replace only the model transport, retaining the normal engine,
	// source handling, SQLite and HTTP boundary.
	modelFactory func() (companion.LanguageModel, error)
}

func newLocalChatService(home *localHomeService) *localChatService {
	ctx, cancel := context.WithCancel(context.Background())
	return &localChatService{home: home, models: newLocalModelStore(home.dataDir), ctx: ctx, cancel: cancel}
}

func (h *localChatService) beginShutdown() {
	if h == nil {
		return
	}
	h.stopping.Store(true)
	// Cancel is idempotent. It is intentionally outside turn/mu: Close must
	// be able to interrupt the model that currently owns turn before waiting
	// to close SQLite or process records.
	if h.cancel != nil {
		h.cancel()
	}
	h.mu.Lock()
	processes := h.processes
	h.mu.Unlock()
	// Do not wait for child process bookkeeping here. Jobs closes admission and
	// cancels existing child contexts while holding its own registry mutex.
	if processes != nil {
		processes.BeginShutdown()
	}
}

func (h *localChatService) close() error {
	return h.closeContext(context.Background())
}

func (h *localChatService) closeContext(ctx context.Context) error {
	h.beginShutdown()
	if h == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		h.closeOnce.Do(func() {
			// An active chat owns turn. Its context was cancelled above, so this
			// wait cannot keep a desktop stop blocked on a provider timeout.
			h.turn.Lock()
			defer h.turn.Unlock()
			h.mu.Lock()
			h.closed = true
			processes, store := h.processes, h.store
			h.mu.Unlock()
			// Jobs owns cancellation and waits for the isolated child result before
			// its durable record is closed. Do not replay any interrupted request.
			if processes != nil {
				h.closeErr = errors.Join(h.closeErr, processes.Close())
			}
			if store != nil {
				h.closeErr = errors.Join(h.closeErr, store.Close())
			}
		})
		close(done)
	}()
	select {
	case <-done:
		return h.closeErr
	case <-ctx.Done():
		return fmt.Errorf("本机服务停止超过等待时限: %w", ctx.Err())
	}
}

func (h *localChatService) conversation() (*companion.Store, companion.Input, error) {
	if h == nil || h.stopping.Load() {
		return nil, companion.Input{}, errors.New("本机服务正在停止")
	}
	record, err := h.home.identity.localIdentity()
	if err != nil || record.LocalOwner == nil {
		return nil, companion.Input{}, errors.New("请先准备本机身份；身份损坏时请保留原文件")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, companion.Input{}, errors.New("本机服务正在停止")
	}
	if h.store == nil {
		store, err := companion.NewStore(filepath.Join(h.home.dataDir, "local-companion", record.FishID))
		if err != nil {
			return nil, companion.Input{}, errors.New("本地聊天存储不可用，原记录保留")
		}
		h.store, h.fishID, h.ownerID = store, record.FishID, record.LocalOwner.ID
	}
	if h.fishID != record.FishID || h.ownerID != record.LocalOwner.ID {
		return nil, companion.Input{}, errors.New("本机身份已变化，请重启独立服务后继续")
	}
	return h.store, companion.Input{Channel: "local", Account: record.FishID, Thread: record.LocalOwner.ID, Sender: record.LocalOwner.ID, SenderName: "主人", SenderNameSource: "local_owner", Mode: "direct", Directed: true}, nil
}

func (h *localChatService) routes(s *Server, mux *http.ServeMux) {
	mux.HandleFunc("GET /api/local/chat", s.protected(h.history))
	mux.HandleFunc("POST /api/local/chat", s.protected(h.send))
	mux.HandleFunc("GET /api/local/model", s.protected(h.modelView))
	mux.HandleFunc("PUT /api/local/model", s.protected(h.modelSave))
}

func (h *localChatService) modelView(w http.ResponseWriter, r *http.Request) {
	view, err := h.models.View()
	if err != nil {
		fail(w, 409, "本机模型配置不可读，原文件保留")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, 200, view)
}

func (h *localChatService) modelSave(w http.ResponseWriter, r *http.Request) {
	var in LocalModelInput
	if !decode(w, r, &in) {
		return
	}
	view, err := h.models.Save(in)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errLocalModelRevision) {
			status = http.StatusConflict
		}
		fail(w, status, err.Error())
		return
	}
	respond(w, 200, view)
}

func (h *localChatService) history(w http.ResponseWriter, r *http.Request) {
	store, in, err := h.conversation()
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			fail(w, 400, "每页可读取 1–100 条；更早记录可按游标续读")
			return
		}
	}
	page, err := store.LocalConversationPage(r.Context(), in, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		fail(w, 409, "聊天记录或游标不可用，请从最新记录重新读取")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, 200, map[string]any{"items": page.Items, "next_cursor": page.NextCursor, "has_more": page.HasMore, "busy": h.busy.Load()})
}

func localConversationConfig(persona string) companion.Config {
	c := companion.Defaults()
	c.Enabled, c.Name, c.Persona = true, "大肥鱼", persona
	c.Permissions = companion.Permissions{ReplyText: true, Remember: true}
	c.Context = companion.ContextDefaults()
	// Maintenance is not silently launched by a newly created local chat.
	// Original events remain on disk and older text can be read on demand.
	c.Context.AutoCompress = false
	c.MemoryDays = 365
	return c
}

func (h *localChatService) send(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.stopping.Load() {
		fail(w, http.StatusServiceUnavailable, "本机服务正在停止，未启动新的聊天或工具")
		return
	}
	var request struct {
		RequestID string `json:"request_id"`
		Text      string `json:"text"`
	}
	if !decode(w, r, &request) {
		return
	}
	if !validID(request.RequestID) || strings.TrimSpace(request.Text) == "" || !utf8.ValidString(request.Text) || len(request.Text) > 64<<10 || strings.ContainsRune(request.Text, 0) {
		fail(w, 400, "请提供稳定请求编号及 1–65536 字节的有效正文；长资料可分批继续")
		return
	}
	if !h.turn.TryLock() {
		fail(w, 409, "当前对话正在生成，请等待后查看记录；不会重复提交")
		return
	}
	defer h.turn.Unlock()
	h.busy.Store(true)
	defer h.busy.Store(false)
	store, in, err := h.conversation()
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	prior, err := store.LocalConversationRequest(r.Context(), in, request.RequestID)
	if err != nil {
		fail(w, 409, "该请求来源不可用或已被遗忘，请使用新请求继续")
		return
	}
	if prior != nil && prior.InputText != request.Text {
		fail(w, 409, "同一请求编号的正文已变化，请使用新的请求编号")
		return
	}
	if prior != nil && prior.Completed {
		respond(w, 200, map[string]any{"request_id": request.RequestID, "messages": prior.Messages, "status": "completed", "reused": true})
		return
	}
	in.Text, in.OriginalText, in.TraceID = request.Text, &request.Text, request.RequestID
	in.MessageTime = time.Now().UTC().Format(time.RFC3339Nano)
	if prior != nil {
		in.MessageTime = prior.OccurredAt
	}
	in.Source = &companion.SourceRef{MessageID: request.RequestID}
	// Save the actual user request before consulting model settings. This uses
	// the normal event/source identity and makes an unavailable model a retryable
	// unfinished turn rather than a lost message or a second event stream.
	if err = store.Observe(in, localConversationConfig(""), time.Now().UTC()); err != nil {
		fail(w, 503, "原消息未能写入本地记录，请先刷新核对")
		return
	}
	run, err := store.OpenLocalToolRun(r.Context(), in, prior == nil)
	if err != nil {
		fail(w, 503, "工具检查点无法核验，未执行本轮工具")
		return
	}
	settings, err := h.models.View()
	if err != nil {
		fail(w, 409, "本机模型配置不可读，原文件保留")
		return
	}
	var model companion.LanguageModel
	if h.modelFactory != nil {
		model, err = h.modelFactory()
	} else {
		model, settings, err = h.models.ClientSnapshot()
	}
	if err != nil {
		fail(w, 503, "请先保存有效的本机模型配置和 Key；没有调用模型")
		return
	}
	if client, ok := model.(interface{ CloseIdleConnections() }); ok {
		defer client.CloseIdleConnections()
	}
	c := localConversationConfig(settings.Persona)
	engine, err := companion.New(c, store, model, "你是大肥鱼，正在 Agent-home-station 软件中与本机主人交谈。按本轮工具目录和主人的授权使用本地记忆、家目录和已连接的应用。软件界面展示已写入本地聊天记录的气泡，不代表微信发送或主人已经读过。不要声称已使用未提供的应用或执行能力。")
	if err != nil {
		fail(w, 503, "本地聊天引擎未能准备，原始记录保留")
		return
	}
	tools := h.tools(store, in)
	tools.run = run
	tools.inspectOnly = !run.Tracked || len(run.Operations) > 0
	engine.Tools = tools
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	stop := context.AfterFunc(h.ctx, cancel)
	defer stop()
	var usageErr error
	ctx = deepseek.WithUsageObserver(deepseek.WithPurpose(ctx, "local_conversation"), func(u deepseek.Usage) {
		dir := filepath.Join(h.home.dataDir, "local-billing")
		err := os.MkdirAll(dir, 0700)
		var ledger *billingStore
		if err == nil {
			ledger, err = newBillingStore(dir)
		}
		if err == nil {
			err = ledger.record(Config{Provider: settings.Provider, BaseURL: settings.BaseURL, Model: settings.Model}, u)
		}
		usageErr = errors.Join(usageErr, err)
	})
	decision, err := engine.Respond(ctx, in)
	if err != nil {
		message, code := "本轮聊天处理未完成，原消息保留，可查看后重试", "local_turn_failed"
		if diagnostic, ok := deepseek.ErrorDiagnostics(err); ok {
			message, code = deepseek.UserMessage(err)+"；原消息保留，可查看后重试", diagnostic.Code
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			message, code = "本轮生成已取消或超时，原消息保留", "local_turn_cancelled"
		}
		respond(w, 502, map[string]any{"error": message, "code": code, "stage": "conversation"})
		return
	}
	if decision.Action != "reply" || len(decision.Messages) == 0 || len(decision.MediaIDs) != 0 || decision.ImageID != "" {
		respond(w, 200, map[string]any{"request_id": request.RequestID, "messages": []string{}, "status": "no_reply", "reason": decision.Reason})
		return
	}
	// A replaced identity or a forgotten source cannot receive a late result.
	_, current, err := h.conversation()
	if err != nil || current.Account != in.Account || current.Sender != in.Sender {
		fail(w, 409, "本机身份已变更，本次结果未写入聊天")
		return
	}
	if proof, ok := decision.GenerationEvidence(); ok {
		if err = engine.ValidateGenerationEvidence(ctx, proof); err != nil {
			fail(w, 409, "对话来源已更正或遗忘，本次结果未写入聊天")
			return
		}
	}
	if err = store.AcceptLocalConversation(ctx, in, decision.Messages, c, time.Now().UTC()); err != nil {
		fail(w, 503, "回复未能写入本地记录，请先刷新核对，原消息保留")
		return
	}
	warning := ""
	if usageErr != nil {
		warning = "回复已保存，但本次用量记录写入失败"
	}
	respond(w, 200, map[string]any{"request_id": request.RequestID, "messages": decision.Messages, "status": "completed", "warning": warning})
}
