package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
}

type stationWechatPolicy struct {
	Enabled bool                 `json:"enabled"`
	Paused  bool                 `json:"paused"`
	Grants  map[string]time.Time `json:"reply_grants"`
}

type stationWechatWorker struct {
	mu     sync.Mutex
	dir    string
	policy stationWechatPolicy
	run    *stationWechatRun
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
	host := &stationWechat{
		stationWechatNative: native, root: filepath.Join(chat.home.dataDir, "apps", wechatapp.ID, "workers"),
		dataDir: chat.home.dataDir, workspaceRoot: chat.home.root,
		installation: entry.InstallationID, models: chat.models, model: stationWechatModel{chat.models},
		ctx: ctx, cancel: cancel, workers: map[string]*stationWechatWorker{}, done: make(chan struct{}),
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

func (s *stationWechat) beginShutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() == nil {
		s.cancel()
		go func() { s.wg.Wait(); close(s.done) }()
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
	return &apphost.ConfiguredPolicy{WorkerEnabled: w.policy.Enabled, ConversationEnabled: len(threads) != 0, Grants: grants, ReplyThreads: threads}
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

func (s *stationWechat) SetConnectionReplyThreads(ctx context.Context, id string, threads []string) (apphost.ConnectionStatus, error) {
	if len(threads) > 100 {
		return apphost.ConnectionStatus{}, apphost.ErrInvalidConnectionPolicy
	}
	grants := make(map[string]time.Time, len(threads))
	now := time.Now().UTC()
	for _, thread := range threads {
		if !validID(thread) || !grants[thread].IsZero() {
			return apphost.ConnectionStatus{}, apphost.ErrInvalidConnectionPolicy
		}
		grants[thread] = now
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
	for thread := range grants {
		if previous, ok := w.policy.Grants[thread]; ok {
			grants[thread] = previous
		}
	}
	if !equalWechatGrants(grants, w.policy.Grants) {
		next := w.policy
		next.Grants = grants
		// Require explicit resume after a scope change on an enabled worker.
		next.Paused = next.Enabled
		if err := w.changePolicy(next); err != nil {
			return view, err
		}
		if err := w.stop(ctx); err != nil {
			return view, err
		}
	}
	view.Configuration, view.Worker = "worker-configured", w.state()
	view.SubmissionsPaused, view.Policy = w.policy.Paused, w.policyView()
	return view, nil
}

func equalWechatGrants(a, b map[string]time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for thread := range a {
		if _, ok := b[thread]; !ok {
			return false
		}
	}
	return true
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
		if err := s.runWorker(ctx, run, w.dir, native, view.Persona, policy); err != nil && ctx.Err() == nil {
			run.state.Store("error")
		}
	}()
	return nil
}

func (s *stationWechat) runWorker(ctx context.Context, run *stationWechatRun, dir string, native *linuxnative.Sender, persona string, policy stationWechatPolicy) error {
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
	character, err := companion.New(config, memory, s.model, persona)
	if err != nil {
		return err
	}
	character.Names = native
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
		StateDir: filepath.Join(dir, "queue"), AllowedThreads: threads, GroupRules: rules,
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
	}, native, character, native)
	if err != nil {
		return err
	}
	return worker.Run(ctx)
}
