package admin

import (
	"context"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/workspacefs"
)

type stationWechatNative interface {
	apphost.Provider
	apphost.AccountSetup
	WorkerSender(context.Context, string) (*linuxnative.Sender, error)
}

type stationWechat struct {
	stationWechatNative
	root, installation     string
	dataDir, workspaceRoot string
	models                 *localModelStore
	model                  companion.LanguageModel
	ctx                    context.Context
	cancel                 context.CancelFunc
	mu                     sync.Mutex
	workers                map[string]*stationWechatWorker
	wg                     sync.WaitGroup
	done                   chan struct{}
	artifacts              *localArtifactStore
	shareOptions           pluginruntime.Options
	contactCursor          cipher.AEAD
}

type stationWechatPolicy struct {
	Enabled          bool                 `json:"enabled"`
	Paused           bool                 `json:"paused"`
	Grants           map[string]time.Time `json:"reply_grants"`
	AttachmentGrants map[string]time.Time `json:"attachment_grants,omitempty"`
	MediaReplyGrants map[string]time.Time `json:"media_reply_grants,omitempty"`
	HistoryGrants    map[string]time.Time `json:"history_grants,omitempty"`
	ContactGrant     time.Time            `json:"contact_grant,omitzero"`
}

type stationWechatWorker struct {
	mu         sync.Mutex
	deliveryMu sync.Mutex
	shareMu    sync.Mutex
	share      *stationShareHost
	dir        string
	policy     stationWechatPolicy
	actions    *actionLedger
	run        *stationWechatRun
}

type stationWechatRun struct {
	state  atomic.Value
	block  atomic.Bool
	cancel context.CancelFunc
	done   chan struct{}
}

func bindStationWechat(registry apphost.Registry, chat *localChatService) (apphost.Registry, *stationWechat, error) {
	entry, ok := registry.Lookup(wechatapp.ID)
	native, supported := entry.Provider.(stationWechatNative)
	if !ok || !supported || entry.Registration != "station-native" {
		return registry, nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	shareOptions := stationShareOptions()
	if pathWithin(chat.home.root, shareOptions.Pythonw) || pathWithin(chat.home.root, shareOptions.WindowsHelper) {
		// Interpreter and isolation helper must be outside the editable fish home.
		shareOptions.Python, shareOptions.Pythonw, shareOptions.WindowsHelper = "", "", ""
	}
	host := &stationWechat{
		stationWechatNative: native, root: filepath.Join(chat.home.dataDir, "apps", wechatapp.ID, "workers"),
		dataDir: chat.home.dataDir, workspaceRoot: chat.home.root,
		installation: entry.InstallationID, models: chat.models, model: stationWechatModel{chat.models},
		ctx: ctx, cancel: cancel, workers: map[string]*stationWechatWorker{}, done: make(chan struct{}),
		artifacts: chat.home.artifacts, shareOptions: shareOptions,
	}
	var err error
	host.contactCursor, err = newStationContactCursor()
	if err != nil {
		cancel()
		return registry, nil, err
	}
	registrations := make([]apphost.Registration, 0, len(registry.Applications()))
	for _, app := range registry.Applications() {
		item, _ := registry.Lookup(app.ID)
		if app.ID == entry.ID {
			item.Provider = host
		}
		registrations = append(registrations, item)
	}
	bound, err := apphost.New(registrations...)
	if err != nil {
		cancel()
		return registry, nil, err
	}
	return bound, host, nil
}

type stationWechatModel struct{ store *localModelStore }

func (m stationWechatModel) Chat(ctx context.Context, system string, history []map[string]string) (string, error) {
	model, err := m.store.Client()
	if err != nil {
		return "", err
	}
	return model.Chat(ctx, system, history)
}

func (m stationWechatModel) ChatFunctions(ctx context.Context, system string, history []map[string]string, tools []mcptools.Tool) (string, error) {
	model, err := m.store.Client()
	if err != nil {
		return "", err
	}
	native, ok := model.(interface {
		ChatFunctions(context.Context, string, []map[string]string, []mcptools.Tool) (string, error)
	})
	if !ok {
		return "", errors.New("station_model_functions_unavailable")
	}
	return native.ChatFunctions(ctx, system, history, tools)
}

func (s *stationWechat) beginShutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() == nil {
		s.cancel()
		go func() {
			s.wg.Wait()
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, worker := range s.workers {
				worker.shareMu.Lock()
				if worker.share != nil {
					_ = worker.share.controller.Close()
					_ = worker.share.store.Close()
				}
				worker.shareMu.Unlock()
			}
			close(s.done)
		}()
	}
}

func (s *stationWechat) closeContext(ctx context.Context) error {
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *stationWechat) restore() {
	// Called during construction, before the handler is published.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		items, err := s.Connections(s.ctx)
		if err != nil {
			return
		}
		for _, item := range items {
			s.restoreWorker(item.ID)
		}
	}()
}

func (s *stationWechat) restoreWorker(id string) {
	w, err := s.worker(id, false)
	if err != nil || w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.policy.Enabled || w.policy.Paused || w.run != nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	native, err := s.WorkerSender(ctx, id)
	if err == nil {
		_ = s.launch(w, native)
	}
}

func (s *stationWechat) worker(id string, create bool) (*stationWechatWorker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return nil, apphost.ErrLifecycleConflict
	}
	if w := s.workers[id]; w != nil {
		return w, nil
	}
	dir := filepath.Join(s.root, id)
	root, err := workspacefs.OpenPath(dir)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil, nil
		}
		root, err = workspacefs.EnsurePath(dir)
	}
	if err != nil {
		return nil, apphost.ErrUnavailable
	}
	defer root.Close()
	policy, err := readStationWechatPolicy(root)
	if err != nil {
		return nil, apphost.ErrUnavailable
	}
	w := &stationWechatWorker{dir: dir, policy: policy}
	s.workers[id] = w
	return w, nil
}

func readStationWechatPolicy(root *os.Root) (stationWechatPolicy, error) {
	const name = "worker.json"
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return stationWechatPolicy{}, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return stationWechatPolicy{}, apphost.ErrUnavailable
	}
	file, err := root.Open(name)
	if err != nil {
		return stationWechatPolicy{}, err
	}
	defer file.Close()
	const maxPolicyBytes = 32 << 10
	data, err := io.ReadAll(io.LimitReader(file, maxPolicyBytes+1))
	var policy stationWechatPolicy
	if err != nil || len(data) > maxPolicyBytes || json.Unmarshal(data, &policy) != nil || len(policy.Grants) > 100 {
		return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
	}
	for thread, granted := range policy.Grants {
		if !validID(thread) || granted.IsZero() {
			return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
		}
	}
	for thread, granted := range policy.AttachmentGrants {
		if policy.Grants[thread].IsZero() || granted.IsZero() || granted.Before(policy.Grants[thread]) {
			return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
		}
	}
	for thread, granted := range policy.HistoryGrants {
		if policy.Grants[thread].IsZero() || granted.IsZero() || granted.Before(policy.Grants[thread]) {
			return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
		}
	}
	for thread, granted := range policy.MediaReplyGrants {
		if policy.AttachmentGrants[thread].IsZero() || granted.IsZero() || granted.Before(policy.AttachmentGrants[thread]) {
			return stationWechatPolicy{}, apphost.ErrInvalidConnectionPolicy
		}
	}
	return policy, nil
}

func (w *stationWechatWorker) save(policy stationWechatPolicy) error {
	data, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	root, err := workspacefs.OpenPath(w.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	// Use the pinned private directory for policy reads and reject redirects.
	if info, err := root.Lstat("worker.json"); err == nil && !info.Mode().IsRegular() || err != nil && !errors.Is(err, os.ErrNotExist) {
		return apphost.ErrUnavailable
	}
	temp := ".worker-" + randomToken()
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	if err := root.Rename(temp, "worker.json"); err != nil {
		return err
	}
	w.policy = policy
	return nil
}

func (w *stationWechatWorker) state() string {
	if w.run != nil {
		select {
		case <-w.run.done:
			if !w.policy.Paused {
				return w.run.state.Load().(string)
			}
		default:
			if w.run.block.Load() {
				return "stopping"
			}
			return w.run.state.Load().(string)
		}
	}
	switch {
	case w.policy.Paused:
		return "paused"
	case w.policy.Enabled:
		return "needs_setup"
	default:
		return "disabled"
	}
}

func (w *stationWechatWorker) stop(ctx context.Context) error {
	if w.run == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	w.run.block.Store(true)
	w.run.cancel()
	select {
	case <-w.run.done:
		w.run = nil
		return nil
	case <-ctx.Done():
		return apphost.ErrLifecycleConflict
	}
}

func (w *stationWechatWorker) policyView() *apphost.ConfiguredPolicy {
	threads := make([]string, 0, len(w.policy.Grants))
	for thread := range w.policy.Grants {
		threads = append(threads, thread)
	}
	slices.Sort(threads)
	grants := []string{}
	if len(threads) != 0 {
		grants = append(grants, "send.text")
	}
	attachments := slices.Sorted(maps.Keys(w.policy.AttachmentGrants))
	if len(attachments) > 0 {
		grants = append(grants, "media.read")
	}
	mediaReplies := slices.Sorted(maps.Keys(w.policy.MediaReplyGrants))
	history := slices.Sorted(maps.Keys(w.policy.HistoryGrants))
	if len(history) > 0 {
		grants = append(grants, "messages.read", "members.read")
	}
	if len(mediaReplies) > 0 {
		grants = append(grants, "send.image", "send.voice", "send.video", "send.file")
	}
	contactRead := !w.policy.ContactGrant.IsZero()
	if contactRead {
		grants = append(grants, "contacts.read")
	}
	return &apphost.ConfiguredPolicy{WorkerEnabled: w.policy.Enabled, ConversationEnabled: len(threads) != 0, ToolsEnabled: len(attachments) > 0 || len(history) > 0 || contactRead, Grants: grants, ReplyThreads: threads, AttachmentThreads: attachments, MediaReplyThreads: mediaReplies, HistoryThreads: history, ContactRead: contactRead}
}

func (s *stationWechat) Inspect(ctx context.Context, id string) (apphost.ConnectionStatus, error) {
	view, err := s.stationWechatNative.Inspect(ctx, id)
	if err != nil {
		return view, err
	}
	w, err := s.worker(id, false)
	if err != nil {
		return view, err
	}
	if w == nil {
		view.Worker = "disabled"
		view.Policy = (&stationWechatWorker{}).policyView()
		return view, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	view.Configuration, view.Worker = "worker-configured", w.state()
	view.SubmissionsPaused, view.Policy = w.policy.Paused, w.policyView()
	return view, nil
}

func (s *stationWechat) SetConnectionPolicy(ctx context.Context, id string, policy apphost.ConnectionPolicyInput) (apphost.ConnectionStatus, error) {
	threads := policy.ReplyThreads
	if len(threads) > 100 {
		return apphost.ConnectionStatus{}, apphost.ErrInvalidConnectionPolicy
	}
	grants := make(map[string]time.Time, len(threads))
	for _, thread := range threads {
		if _, duplicate := grants[thread]; !validID(thread) || duplicate {
			return apphost.ConnectionStatus{}, apphost.ErrInvalidConnectionPolicy
		}
		grants[thread] = time.Time{}
	}
	view, err := s.stationWechatNative.Inspect(ctx, id)
	if err != nil {
		return view, err
	}
	w, err := s.worker(id, true)
	if err != nil {
		return view, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if s.ctx.Err() != nil || ctx.Err() != nil {
		return view, apphost.ErrLifecycleConflict
	}
	now := time.Now().UTC()
	for thread := range grants {
		if previous, ok := w.policy.Grants[thread]; ok {
			grants[thread] = previous
		} else {
			grants[thread] = now
		}
	}
	attachments, err := stationScopedGrants(grants, w.policy.AttachmentGrants, policy.AttachmentThreads, now)
	if err != nil {
		return view, err
	}
	mediaReplies, err := stationScopedGrants(attachments, w.policy.MediaReplyGrants, policy.MediaReplyThreads, now)
	if err != nil {
		return view, err
	}
	history, err := stationScopedGrants(grants, w.policy.HistoryGrants, policy.HistoryThreads, now)
	if err != nil {
		return view, err
	}
	next := w.policy
	if policy.ContactRead != nil {
		if !*policy.ContactRead {
			next.ContactGrant = time.Time{}
		} else if next.ContactGrant.IsZero() {
			next.ContactGrant = now
		}
	}
	workerScopeChanged := !maps.Equal(grants, w.policy.Grants) || !maps.Equal(attachments, w.policy.AttachmentGrants) || !maps.Equal(mediaReplies, w.policy.MediaReplyGrants) || !maps.Equal(history, w.policy.HistoryGrants)
	if workerScopeChanged {
		next.Grants, next.AttachmentGrants, next.MediaReplyGrants, next.HistoryGrants = grants, attachments, mediaReplies, history
		// Require explicit resume after a scope change on an enabled worker.
		next.Paused = next.Enabled
		if err := w.changePolicy(next); err != nil {
			return view, err
		}
		if err := w.stop(ctx); err != nil {
			return view, err
		}
	} else if next.ContactGrant != w.policy.ContactGrant {
		if err := w.save(next); err != nil {
			return view, err
		}
	}
	view.Configuration, view.Worker = "worker-configured", w.state()
	view.SubmissionsPaused, view.Policy = w.policy.Paused, w.policyView()
	return view, nil
}

func stationScopedGrants(parent, previous map[string]time.Time, requested *[]string, now time.Time) (map[string]time.Time, error) {
	result := map[string]time.Time{}
	if requested == nil {
		for thread, grant := range previous {
			if !parent[thread].IsZero() {
				result[thread] = grant
			}
		}
		return result, nil
	}
	for _, thread := range *requested {
		if parent[thread].IsZero() || !result[thread].IsZero() {
			return nil, apphost.ErrInvalidConnectionPolicy
		}
		grant := previous[thread]
		if grant.IsZero() {
			grant = now
		}
		result[thread] = grant
	}
	return result, nil
}

func (w *stationWechatWorker) changePolicy(next stationWechatPolicy) error {
	var wasBlocked bool
	if w.run != nil {
		wasBlocked = w.run.block.Swap(true)
	}
	if err := w.save(next); err != nil {
		if w.run != nil {
			w.run.block.Store(wasBlocked)
		}
		return err
	}
	return nil
}

func (s *stationWechat) SetConnectionRunning(ctx context.Context, id string, running bool) (apphost.ConnectionLifecycle, error) {
	native, err := s.WorkerSender(ctx, id)
	if err != nil {
		return apphost.ConnectionLifecycle{}, err
	}
	w, err := s.worker(id, true)
	if err != nil {
		return apphost.ConnectionLifecycle{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if s.ctx.Err() != nil || ctx.Err() != nil {
		return apphost.ConnectionLifecycle{}, apphost.ErrLifecycleConflict
	}
	if running {
		view, modelErr := s.models.View()
		if modelErr != nil || !view.KeyConfigured || len(w.policy.Grants) == 0 {
			return apphost.ConnectionLifecycle{}, apphost.ErrLifecycleNotReady
		}
		if state := w.state(); state != "running" && state != "waiting_login" {
			if err := w.stop(ctx); err != nil {
				return apphost.ConnectionLifecycle{}, err
			}
			next := w.policy
			next.Enabled, next.Paused = true, false
			if err := w.save(next); err != nil {
				return apphost.ConnectionLifecycle{}, err
			}
			if err := s.launch(w, native); err != nil {
				return apphost.ConnectionLifecycle{}, err
			}
		}
	} else {
		next := w.policy
		next.Paused = true
		if err := w.changePolicy(next); err != nil {
			return apphost.ConnectionLifecycle{}, err
		}
		if err := w.stop(ctx); err != nil {
			return apphost.ConnectionLifecycle{}, err
		}
	}
	return apphost.ConnectionLifecycle{
		ConnectionRef: s.connectionRef(id, native.Account()),
		Worker:        w.state(), WorkerEnabled: w.policy.Enabled, SubmissionsPaused: w.policy.Paused,
	}, nil
}

func (s *stationWechat) launch(w *stationWechatWorker, native *linuxnative.Sender) error {
	view, err := s.models.View()
	if err != nil || !view.KeyConfigured || len(w.policy.Grants) == 0 {
		return apphost.ErrLifecycleNotReady
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return apphost.ErrLifecycleConflict
	}
	ctx, cancel := context.WithCancel(s.ctx)
	run := &stationWechatRun{cancel: cancel, done: make(chan struct{})}
	run.state.Store("waiting_login")
	w.run = run
	policy := w.policy
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(run.done)
		defer cancel()
		if err := s.runWorker(ctx, run, w, native, view.Persona, policy); err != nil && ctx.Err() == nil {
			run.state.Store("error")
		}
	}()
	return nil
}

func (s *stationWechat) runWorker(ctx context.Context, run *stationWechatRun, station *stationWechatWorker, native *linuxnative.Sender, persona string, policy stationWechatPolicy) error {
	dir := station.dir
	for {
		statusCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		status, err := native.Status(statusCtx)
		cancel()
		if err == nil && status.Authenticated && (status.Connection == "online" || status.Connection == "connected") {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	memory, err := companion.NewStore(filepath.Join(dir, "memory"))
	if err != nil {
		return err
	}
	defer memory.Close()
	config := companion.Defaults()
	config.Enabled, config.Name = true, "大肥鱼"
	config.Permissions = companion.Permissions{ReplyText: true, Remember: true}
	mediaReplies := len(policy.MediaReplyGrants) != 0
	config.Permissions.SendImage, config.Permissions.SendVideo, config.Permissions.SendVoice, config.Permissions.SendFile = mediaReplies, mediaReplies, mediaReplies, mediaReplies
	character, err := companion.New(config, memory, s.model, persona)
	if err != nil {
		return err
	}
	character.Names = native
	attachment := &stationAttachmentTools{store: memory, native: native, dir: dir, grants: policy.AttachmentGrants, replies: policy.MediaReplyGrants, reads: policy.HistoryGrants, blocked: run.block.Load}
	attachment.shareHost = func() (*stationShareHost, error) { return s.shareHost(station, native.Account()) }
	if s.artifacts != nil {
		attachment.work = &stationWorkAccess{artifacts: s.artifacts, dir: dir, grants: policy.MediaReplyGrants}
	}
	if len(policy.AttachmentGrants) != 0 || len(policy.HistoryGrants) != 0 {
		character.Tools = attachment
	}
	threads := make([]string, 0, len(policy.Grants))
	rules := make(map[string]bot.GroupRule)
	for thread := range policy.Grants {
		threads = append(threads, thread)
		if strings.HasSuffix(thread, "@chatroom") {
			rules[thread] = bot.GroupRule{Mode: "mention"}
		}
	}
	options := bot.DefaultOptions()
	options.AcknowledgeTools, options.ReadImages, options.ReadVideos, options.RandomDelay = false, false, false, false
	worker, err := bot.New(bot.Config{
		DeliveryCancelled:  station.deliveryCancelled,
		WithDeliverySubmit: station.withDeliverySubmit,
		NextMediaResume:    station.nextMediaResume,
		MediaResumeResult:  station.mediaResumeResult,
		StateDir:           filepath.Join(dir, "queue"), AllowedThreads: threads, GroupRules: rules,
		Options: &options, Prefix: "/ai ",
		Log: func(event string) {
			if event == "bot_started_history_skipped" || event == "bot_resumed_saved_cursor" {
				run.state.Store("running")
			}
		},
		BeforeSubmit: func(ctx context.Context) error {
			if run.block.Load() {
				return apphost.ErrLifecycleConflict
			}
			return ctx.Err()
		},
		AuthorizeThread: func(ctx context.Context, thread string) (bool, error) {
			return !run.block.Load() && !policy.Grants[thread].IsZero(), ctx.Err()
		},
		AuthorizeReply: func(in companion.Input) bool {
			granted := policy.Grants[in.Thread]
			when, err := time.Parse(time.RFC3339Nano, in.MessageTime)
			return in.Account == native.Account() && !granted.IsZero() && err == nil && !when.Before(granted)
		},
		AuthorizeMedia:      attachment.replyAllowed,
		PrepareMediaSource:  attachment.stageReply,
		AuthorizeMediaAsset: attachment.workAllowed,
	}, native, character, native)
	if err != nil {
		return err
	}
	return worker.Run(ctx)
}
