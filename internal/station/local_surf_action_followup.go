package station

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/workspacefs"
)

const surfContinuationPlugin = "station-surf-actions"

type surfActionWait struct {
	Job        string    `json:"job"`
	Connection string    `json:"connection"`
	Next       time.Time `json:"next"`
	State      string    `json:"state"`
	Reason     string    `json:"reason"`
	Failures   int       `json:"failures"`
	Lease      string    `json:"lease,omitempty"`
}

type localSurfActionWorker struct {
	service   *localChatService
	ctx       context.Context
	cancel    context.CancelFunc
	started   atomic.Bool
	rescan    atomic.Bool
	snapshot  atomic.Pointer[surfActionSnapshot]
	mu        sync.Mutex
	wake      chan struct{}
	done      chan struct{}
	scanActor string
	scanAfter int64
	nextScan  time.Time
}

type surfActionSummary struct {
	AsOf     time.Time `json:"as_of"`
	Waiting  int       `json:"waiting"`
	Checking int       `json:"checking"`
	Blocked  int       `json:"blocked"`
	Next     time.Time `json:"next,omitzero"`
}
type surfActionSnapshot struct {
	Owner       string
	At          time.Time
	Connections map[string]surfActionSummary
}

func (w *localSurfActionWorker) summary(owner, connection string) *surfActionSummary {
	if w == nil {
		return nil
	}
	snapshot := w.snapshot.Load()
	if snapshot == nil || snapshot.Owner != owner {
		return nil
	}
	row := snapshot.Connections[connection]
	row.AsOf = snapshot.At
	return &row
}

func (w *localSurfActionWorker) cache(ctx context.Context, store *pluginhost.Store, actor pluginhost.Invocation) {
	_, rows, err := readSurfWaits(ctx, store, actor)
	if err != nil {
		return
	}
	snapshot := &surfActionSnapshot{Owner: surfWaitNamespace(actor), At: time.Now().UTC(), Connections: map[string]surfActionSummary{}}
	for _, row := range rows {
		count := snapshot.Connections[row.Connection]
		switch row.State {
		case "checking":
			count.Checking++
		case "blocked":
			count.Blocked++
		default:
			count.Waiting++
		}
		if count.Next.IsZero() || row.Next.Before(count.Next) {
			count.Next = row.Next
		}
		snapshot.Connections[row.Connection] = count
	}
	w.snapshot.Store(snapshot)
}

func newLocalSurfActionWorker(h *localChatService) *localSurfActionWorker {
	ctx, cancel := context.WithCancel(h.ctx)
	return &localSurfActionWorker{service: h, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (w *localSurfActionWorker) notify() {
	if w == nil {
		return
	}
	w.rescan.Store(true)
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (h *localChatService) startSurfActionContinuations() {
	w := h.surfActions
	if w == nil || !w.started.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer close(w.done)
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for {
			select {
			case <-w.ctx.Done():
				return
			case <-timer.C:
			case <-w.wake:
			}
			w.tick(w.ctx, time.Now().UTC())
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(time.Second)
		}
	}()
}

func surfWaitNamespace(actor pluginhost.Invocation) string {
	return localCheckpointKey("surf-action-owner-v1", actor.Account, actor.Requester)
}

func readSurfWaits(ctx context.Context, store *pluginhost.Store, actor pluginhost.Invocation) (pluginhost.HostState, map[string]surfActionWait, error) {
	state, err := store.ReadHostState(ctx, surfContinuationPlugin, surfWaitNamespace(actor))
	rows := map[string]surfActionWait{}
	if err != nil {
		return state, rows, err
	}
	if state.Revision > 0 && state.SourceHash != surfWaitNamespace(actor) {
		return state, rows, pluginhost.ErrHostStateCorrupt
	}
	for key, raw := range state.Records {
		var row surfActionWait
		if strictLocalMemoryDecode(raw, &row) != nil || row.Job != key || !localDouyinPublicationHex(key) || !artifactID(row.Connection, "wechat-") || row.Next.IsZero() || row.Failures < 0 || row.Failures > 10 ||
			!slices.Contains([]string{"waiting", "checking", "blocked"}, row.State) || row.Lease != "" && !identity.ValidPathID(row.Lease) {
			return state, rows, pluginhost.ErrHostStateCorrupt
		}
		rows[key] = row
	}
	return state, rows, nil
}

func editSurfWaits(ctx context.Context, store *pluginhost.Store, actor pluginhost.Invocation, change func(map[string]surfActionWait) error) error {
	for attempt := 0; attempt < 4; attempt++ {
		state, rows, err := readSurfWaits(ctx, store, actor)
		if err != nil {
			return err
		}
		if err := change(rows); err != nil {
			return err
		}
		puts := map[string]json.RawMessage{}
		deletes := []string{}
		for key, row := range rows {
			raw, _ := json.Marshal(row)
			if !bytes.Equal(raw, state.Records[key]) {
				puts[key] = raw
			}
		}
		for key := range state.Records {
			if _, ok := rows[key]; !ok {
				deletes = append(deletes, key)
			}
		}
		if state.Revision == 0 {
			_, err = store.InitializeHostState(ctx, surfContinuationPlugin, surfWaitNamespace(actor), surfWaitNamespace(actor), puts)
		} else if len(puts) > 0 || len(deletes) > 0 {
			_, err = store.UpdateHostState(ctx, surfContinuationPlugin, surfWaitNamespace(actor), state.Revision, puts, deletes)
		}
		if errors.Is(err, pluginhost.ErrRevision) {
			continue
		}
		return err
	}
	return pluginhost.ErrRevision
}

// This guard is re-used by the provider at its actual effect boundary. Pausing
// the parent worker or logging out cannot race a queued browser submission.
func (h *localChatService) surfContinuationActive(ctx context.Context, job pluginhost.Job, _ apphost.InteractionDraft) error {
	if h.stopping.Load() || h.wechat == nil || ctx.Err() != nil {
		return apphost.ErrInteractionDenied
	}
	origin, err := surfAppJobOrigin(job)
	if err != nil || !artifactID(origin.Connection, "wechat-") {
		return apphost.ErrInteractionDenied
	}
	worker, err := h.wechat.worker(origin.Connection, false)
	if err != nil || worker == nil {
		return apphost.ErrInteractionDenied
	}
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		return apphost.ErrInteractionDenied
	}
	defer root.Close()
	policy, err := readStationWechatPolicy(root)
	if err != nil || !policy.Enabled || policy.Paused || worker.cutoverBlocked.Load() {
		return apphost.ErrInteractionDenied
	}
	if !worker.mu.TryLock() {
		return apphost.ErrInteractionDenied
	}
	blocked := worker.run != nil && worker.run.Blocked()
	worker.mu.Unlock()
	if blocked {
		return apphost.ErrInteractionDenied
	}
	native, err := h.wechat.WorkerSender(ctx, origin.Connection)
	if err != nil || native.Account() != origin.Account {
		return apphost.ErrInteractionDenied
	}
	status, err := native.Status(ctx)
	if err != nil || !status.Authenticated || status.Connection != "online" && status.Connection != "connected" {
		return apphost.ErrInteractionDenied
	}
	activation, err := h.wechat.legacyActivationCheck(ctx, origin.Connection, native, root, policy)
	if err != nil || activation != nil && !activation() {
		return apphost.ErrInteractionDenied
	}
	return ctx.Err()
}

func (w *localSurfActionWorker) tick(ctx context.Context, now time.Time) {
	if !w.mu.TryLock() {
		return
	}
	defer w.mu.Unlock()
	h := w.service
	if ctx.Err() != nil || h.stopping.Load() || h.wechat == nil {
		return
	}
	host, actor, err := h.interactionHostWithGuard(h.surfContinuationActive)
	if err != nil {
		return
	}
	project, err := h.projectTools.forFish(actor.Account)
	if err != nil {
		return
	}
	store := project.store
	defer w.cache(ctx, store, actor)
	key := surfWaitNamespace(actor)
	if w.scanActor != key {
		w.scanActor = key
		w.scanAfter = 0
		w.nextScan = time.Time{}
	}
	if w.rescan.Swap(false) {
		w.nextScan = time.Time{}
	}
	if !now.Before(w.nextScan) {
		page, err := store.PendingApplicationJobs(ctx, actor.Requester, "wechat_surf", w.scanAfter, 100)
		if err != nil {
			return
		}
		if len(page.Items) > 0 {
			err = editSurfWaits(ctx, store, actor, func(rows map[string]surfActionWait) error {
				for _, job := range page.Items {
					origin, err := surfAppJobOrigin(job)
					if err != nil || origin.Fish != actor.Account || origin.Owner != actor.Requester || !artifactID(origin.Connection, "wechat-") || job.CancelRequested {
						continue
					}
					if _, exists := rows[job.ID]; !exists {
						rows[job.ID] = surfActionWait{Job: job.ID, Connection: origin.Connection, Next: now, State: "waiting", Reason: "original_task"}
					}
				}
				return nil
			})
			if err != nil && !errors.Is(err, pluginhost.ErrHostStateSize) {
				return
			}
			if errors.Is(err, pluginhost.ErrHostStateSize) {
				w.nextScan = now.Add(5 * time.Second)
			}
		}
		if err == nil {
			w.scanAfter = page.Next
		}
		if err == nil && !page.HasMore {
			w.scanAfter = 0
			w.nextScan = now.Add(15 * time.Second)
		}
	}
	_, rows, err := readSurfWaits(ctx, store, actor)
	if err != nil {
		return
	}
	due := []surfActionWait{}
	for _, row := range rows {
		if !now.Before(row.Next) {
			due = append(due, row)
		}
	}
	slices.SortFunc(due, func(a, b surfActionWait) int {
		if n := a.Next.Compare(b.Next); n != 0 {
			return n
		}
		return cmp.Compare(a.Job, b.Job)
	})
	if len(due) == 0 {
		return
	}
	w.advance(ctx, store, host, actor, due[0], now)
}

func (w *localSurfActionWorker) advance(ctx context.Context, store *pluginhost.Store, host *apphost.Interactor, actor pluginhost.Invocation, row surfActionWait, now time.Time) {
	plan, err := host.PlanContinuation(ctx, actor, row.Job)
	if err != nil {
		w.deferWait(ctx, store, actor, row, now, "source_or_connection_unavailable")
		return
	}
	if plan.Action == "" {
		_ = editSurfWaits(ctx, store, actor, func(rows map[string]surfActionWait) error {
			if current, ok := rows[row.Job]; ok && current == row {
				delete(rows, row.Job)
			}
			return nil
		})
		return
	}
	if plan.NotBefore != "" {
		at, err := time.Parse(time.RFC3339Nano, plan.NotBefore)
		if err != nil {
			return
		}
		if now.Before(at) {
			_ = editSurfWaits(ctx, store, actor, func(rows map[string]surfActionWait) error {
				if current, ok := rows[row.Job]; ok && current == row {
					row.Next = at
					row.State = "waiting"
					row.Reason = "platform_spacing"
					row.Lease = ""
					rows[row.Job] = row
				}
				return nil
			})
			return
		}
	}
	token := identity.RandomToken()
	err = editSurfWaits(ctx, store, actor, func(rows map[string]surfActionWait) error {
		current, ok := rows[row.Job]
		if !ok || current != row || now.Before(current.Next) {
			return pluginhost.ErrRevision
		}
		current.State = "checking"
		current.Reason = plan.Action
		current.Lease = token
		current.Next = now.Add(time.Minute)
		rows[row.Job] = current
		return nil
	})
	if err != nil {
		return
	}
	w.cache(ctx, store, actor)
	bounded, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	view, actionErr := host.Action(bounded, actor, row.Job, plan.Action)
	// Finish only our own lease. A restarted worker may already own the record.
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer finishCancel()
	_ = editSurfWaits(finishCtx, store, actor, func(rows map[string]surfActionWait) error {
		current, ok := rows[row.Job]
		if !ok || current.Lease != token {
			return pluginhost.ErrRevision
		}
		if actionErr == nil && slices.Contains([]string{"applied", "failed", "cancelled"}, view.State) {
			delete(rows, row.Job)
			return nil
		}
		current.Lease = ""
		current.Failures = min(10, current.Failures+1)
		current.State = "waiting"
		current.Reason = "reconcile_original"
		delay := min(5*time.Minute, (time.Duration(1)<<min(5, current.Failures))*5*time.Second)
		current.Next = now.Add(delay)
		if view.NextAttemptAt != "" {
			if at, err := time.Parse(time.RFC3339Nano, view.NextAttemptAt); err == nil && at.After(now) {
				current.Next = at
				current.Failures = 0
				current.Reason = "platform_spacing"
			}
		}
		if actionErr != nil {
			current.Reason = "operation_unavailable"
		}
		rows[row.Job] = current
		return nil
	})
}

func (w *localSurfActionWorker) deferWait(ctx context.Context, store *pluginhost.Store, actor pluginhost.Invocation, row surfActionWait, now time.Time, reason string) {
	_ = editSurfWaits(ctx, store, actor, func(rows map[string]surfActionWait) error {
		if current, ok := rows[row.Job]; ok && current == row {
			row.Lease = ""
			row.State = "blocked"
			row.Reason = reason
			row.Failures = min(10, row.Failures+1)
			row.Next = now.Add(time.Minute)
			rows[row.Job] = row
		}
		return nil
	})
}
