package adapter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/sending"
)

// WorkerSender grants the station host an account-scoped native transport only
// after checking the current roster. Every subsequent request rechecks ownership.
func (p *stationNativeProvider) WorkerSender(ctx context.Context, connectionID string) (*linuxnative.Sender, error) {
	sender, err := p.loginAccount(ctx, connectionID, false)
	if err != nil {
		return nil, err
	}
	sender.GuardRequests(p.verify)
	return sender, nil
}

// WorkerLifecycle owns admission and shutdown of the station's WeChat workers.
// The station supplies persisted policy and the work to run for each account.
type WorkerLifecycle struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	wg      sync.WaitGroup
	done    chan struct{}
	cleanup func()
}

func NewWorkerLifecycle(cleanup func()) *WorkerLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &WorkerLifecycle{ctx: ctx, cancel: cancel, done: make(chan struct{}), cleanup: cleanup}
}

func (l *WorkerLifecycle) Context() context.Context { return l.ctx }

func (l *WorkerLifecycle) start(work func()) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx.Err() != nil {
		return false
	}
	l.wg.Go(work)
	return true
}

func (l *WorkerLifecycle) Restore(connections func(context.Context) ([]apphost.Connection, error), restore func(string)) {
	l.start(func() {
		items, err := connections(l.ctx)
		if err != nil {
			return
		}
		for _, item := range items {
			if l.ctx.Err() != nil {
				return
			}
			restore(item.ID)
		}
	})
}

func (l *WorkerLifecycle) BeginShutdown() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx.Err() != nil {
		return
	}
	l.cancel()
	go func() {
		l.wg.Wait()
		if l.cleanup != nil {
			l.cleanup()
		}
		close(l.done)
	}()
}

func (l *WorkerLifecycle) CloseContext(ctx context.Context) error {
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WorkerRun is one account's in-memory run. Its submission fence is separate
// from the host's durable pause so a failed policy write can restore the fence.
type WorkerRun struct {
	state  atomic.Value
	block  atomic.Bool
	cancel context.CancelFunc
	done   chan struct{}
}

func (l *WorkerLifecycle) Start(work func(context.Context, *WorkerRun) error) (*WorkerRun, error) {
	ctx, cancel := context.WithCancel(l.ctx)
	run := &WorkerRun{cancel: cancel, done: make(chan struct{})}
	run.state.Store("waiting_login")
	if !l.start(func() {
		defer close(run.done)
		defer cancel()
		if err := work(ctx, run); err != nil && ctx.Err() == nil {
			run.state.Store("error")
		}
	}) {
		cancel()
		return nil, apphost.ErrLifecycleConflict
	}
	return run, nil
}

func (r *WorkerRun) Status(paused bool) string {
	select {
	case <-r.done:
		if paused {
			return ""
		}
	default:
		if r.Blocked() {
			return "stopping"
		}
	}
	return r.state.Load().(string)
}

func (r *WorkerRun) Blocked() bool                { return r.block.Load() }
func (r *WorkerRun) SetBlocked(blocked bool) bool { return r.block.Swap(blocked) }
func (r *WorkerRun) Done() <-chan struct{}        { return r.done }

func (r *WorkerRun) Cancel() {
	r.SetBlocked(true)
	r.cancel()
}

func (r *WorkerRun) BlockCutover() {
	r.SetBlocked(true)
	r.state.Store("cutover_blocked")
	r.cancel()
}

func (r *WorkerRun) Drain(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	r.Cancel()
	select {
	case <-r.Done():
		return nil
	case <-ctx.Done():
		return apphost.ErrLifecycleConflict
	}
}

func WaitWorkerLogin(ctx context.Context, native *linuxnative.Sender) error {
	for {
		statusCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		status, err := native.Status(statusCtx)
		cancel()
		if err == nil && status.Authenticated && (status.Connection == "online" || status.Connection == "connected") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// WorkerQueue supplies the host-owned dependencies for one WeChat run.
// The adapter owns defaults and inbound policy. The host retains storage,
// artifact authorization, and the fence used to serialize native submissions.
type WorkerQueue struct {
	ProtectQuote      func(context.Context, linuxnative.Message) bool
	SurfTask          func(time.Time) (*bot.SurfTask, error)
	Record            func(bot.Record)
	StateDir          string
	Groups            GroupPolicy
	AllowMediaReplies bool
	Memory            *companion.Store
	Model             companion.LanguageModel
	Persona           string
	Tools             companion.ToolAccess
	MediaObserver     WorkerMediaObserver

	ReadGroupShare       func(context.Context, companion.Input, linuxnative.Message) (string, companion.MediaObservationEvidence, error)
	DeliveryCancelled    func(companion.Input) (bool, error)
	WithDeliverySubmit   func(context.Context, companion.Input, func() error) error
	NextMediaResume      func(time.Time) (*sending.MediaResumeRequest, error)
	MediaResumeResult    func(sending.MediaResumeRequest, sending.MediaResumeResult) error
	AuthorizeMedia       func(companion.Input) bool
	PrepareMediaSource   func(context.Context, companion.Input, string, string) (string, error)
	AuthorizeMediaAsset  func(companion.Input, string, string) bool
	AuthorizeMediaSubmit func(context.Context, companion.Input, string, string) bool
}

type WorkerMediaObserver interface {
	Analyze(context.Context, linuxnative.Message) (string, error)
	AnalyzeWithEvidence(context.Context, linuxnative.Message) (string, companion.MediaObservationEvidence, error)
	HandlesType(int) bool
	SupportsMedia(context.Context, linuxnative.Message) bool
}

func (r *WorkerRun) RunQueue(ctx context.Context, native *linuxnative.Sender, queue WorkerQueue, checkActivation func() error) error {
	if native == nil || checkActivation == nil || queue.WithDeliverySubmit == nil {
		return apphost.ErrLifecycleNotReady
	}
	characterConfig, config := r.queueConfig(native.Account(), queue, checkActivation)
	character, err := companion.New(characterConfig, queue.Memory, queue.Model, queue.Persona)
	if err != nil {
		return err
	}
	character.Names, character.Tools = native, queue.Tools
	character.ConversationPreference = func(in companion.Input) (string, bool) {
		if r.Blocked() {
			return "", false
		}
		return queue.Groups.ConversationNote(native.Account(), in)
	}
	worker, err := bot.New(config, activationCheckedSource{Sender: native, check: checkActivation}, character, native)
	if err != nil {
		return err
	}
	return worker.Run(ctx)
}

func (r *WorkerRun) queueConfig(account string, queue WorkerQueue, checkActivation func() error) (companion.Config, bot.Config) {
	character := companion.Defaults()
	character.Enabled, character.Name = true, "大肥鱼"
	// The application model supplies the live main and application personas.
	character.Persona = "使用宿主在本轮提供的主角色设定，并结合当前应用的子角色补充。"
	character.Permissions = companion.Permissions{
		ReplyText: true, Remember: true, SendImage: queue.AllowMediaReplies,
		SendVideo: queue.AllowMediaReplies, SendVoice: queue.AllowMediaReplies, SendFile: queue.AllowMediaReplies,
	}
	groups := queue.Groups
	threads, rules := groups.Participation(&character)
	config := bot.Config{
		ProtectQuote: queue.ProtectQuote,
		SurfTask:     queue.SurfTask,
		StateDir:     queue.StateDir, Record: queue.Record, AllowedThreads: threads, GroupRules: rules,
		ScopedAnalyzer:    queue.MediaObserver,
		DeliveryCancelled: queue.DeliveryCancelled, WithDeliverySubmit: queue.WithDeliverySubmit,
		NextMediaResume: queue.NextMediaResume, MediaResumeResult: queue.MediaResumeResult,
		AuthorizeMedia: queue.AuthorizeMedia, PrepareMediaSource: queue.PrepareMediaSource,
		AuthorizeMediaAsset: queue.AuthorizeMediaAsset, AuthorizeMediaSubmit: queue.AuthorizeMediaSubmit,
		OnDemandAttachment: func(m linuxnative.Message) bool {
			kind, _ := AttachmentKind(m)
			grant := groups.Attachments[m.ThreadID]
			stamp, err := time.Parse(time.RFC3339Nano, m.CreatedAt)
			return kind == "file" && m.Direction == "incoming" && !r.Blocked() && m.AccountID == account && !grant.IsZero() && err == nil && !stamp.Before(grant)
		},
		AuthorizeReply: func(in companion.Input) bool { return groups.AllowsReply(account, in) },
	}
	if queue.ReadGroupShare != nil {
		config.PreferredGroupShare = func(m linuxnative.Message) bool {
			return !r.Blocked() && groups.ShareCandidate(account, m)
		}
		config.ReadPreferredGroupShare = func(ctx context.Context, in companion.Input, m linuxnative.Message) (string, companion.MediaObservationEvidence, error) {
			if !config.PreferredGroupShare(m) {
				return "", companion.MediaObservationEvidence{}, errors.New("share_group_policy_changed")
			}
			return queue.ReadGroupShare(ctx, in, m)
		}
	}
	options := bot.DefaultOptions()
	options.AcknowledgeTools, options.ReadImages, options.ReadVideos, options.RandomDelay = false, false, false, false
	config.Options, config.Prefix = &options, "/ai "
	config.Log = func(event string) {
		if event == "bot_started_history_skipped" || event == "bot_resumed_saved_cursor" {
			r.state.Store("running")
		}
	}
	config.BeforeSubmit = func(ctx context.Context) error {
		if r.Blocked() {
			return apphost.ErrLifecycleConflict
		}
		if err := checkActivation(); err != nil {
			return err
		}
		return ctx.Err()
	}
	config.AuthorizeThread = func(ctx context.Context, thread string) (bool, error) {
		if err := checkActivation(); err != nil {
			return false, err
		}
		if r.Blocked() {
			return false, ctx.Err()
		}
		return !groups.Replies[thread].IsZero(), ctx.Err()
	}
	withDeliverySubmit := config.WithDeliverySubmit
	config.WithDeliverySubmit = func(ctx context.Context, in companion.Input, submit func() error) error {
		// Recheck while the durable cancellation fence is held, immediately
		// before handing the original delivery to the native account.
		return withDeliverySubmit(ctx, in, func() error {
			if err := checkActivation(); err != nil {
				return err
			}
			return submit()
		})
	}
	return character, config
}

type activationCheckedSource struct {
	*linuxnative.Sender
	check func() error
}

func (s activationCheckedSource) Events(ctx context.Context, after string) (linuxnative.EventPage, error) {
	if err := s.check(); err != nil {
		return linuxnative.EventPage{}, err
	}
	return s.Sender.Events(ctx, after)
}
