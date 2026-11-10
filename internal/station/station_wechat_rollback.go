package station

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/websession"
)

type legacyRollbackView struct {
	State     string                      `json:"state"`
	Eligible  bool                        `json:"eligible"`
	FrozenAt  time.Time                   `json:"frozen_at,omitzero"`
	Snapshot  string                      `json:"snapshot,omitempty"`
	NextAfter string                      `json:"next_after,omitempty"`
	Native    string                      `json:"native,omitempty"`
	Items     []wechatapp.RollbackReceipt `json:"items"`
}

type legacyRollbackTarget struct {
	legacyRollbackView
	worker  *stationWechatWorker
	ref     apphost.ConnectionRef
	dataset string
}

func (s *stationWechat) legacyRollbackHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	freeze := r.Method == http.MethodPost
	if freeze && !websession.Decode(w, r, &struct{}{}) {
		return
	}
	q := r.URL.Query()
	limit := 20
	if raw := q.Get("limit"); raw != "" {
		var err error
		if limit, err = strconv.Atoi(raw); err != nil || limit < 1 || limit > 200 {
			websession.Fail(w, http.StatusBadRequest, "每页请输入 1 到 200 条")
			return
		}
	}
	cursor := q.Get("after")
	revision, after, _ := strings.Cut(cursor, ":")
	if cursor != "" && (!legacyMemoryDigestValid(revision) || !identity.ValidPathID(after)) {
		websession.Fail(w, http.StatusBadRequest, "回退核对游标无效，请从第一页读取")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	view, err := s.legacyRollback(ctx, r.PathValue("connection"), freeze, revision, after, limit)
	switch {
	case errors.Is(err, apphost.ErrLifecycleCutover):
		websession.Fail(w, http.StatusConflict, "尚未确认该连接的接续，无法冻结回退")
	case errors.Is(err, errLegacyImportChanged):
		websession.Fail(w, http.StatusConflict, "本地投递记录已变化，请从第一页重新核对")
	case err != nil:
		legacyImportFailure(w, err)
	case freeze && view.State == "draining":
		websession.Respond(w, http.StatusAccepted, view)
	default:
		websession.Respond(w, http.StatusOK, view)
	}
}

func (s *stationWechat) legacyRollback(ctx context.Context, id string, freeze bool, revision, after string, limit int) (legacyRollbackView, error) {
	target, err := s.rollbackTarget(ctx, id, freeze)
	if err != nil || freeze || target.State != "frozen" {
		return target.legacyRollbackView, err
	}
	return s.rollbackReceiptPage(ctx, target, revision, after, limit)
}

func (s *stationWechat) rollbackTarget(ctx context.Context, id string, freeze bool) (legacyRollbackTarget, error) {
	view := legacyRollbackTarget{legacyRollbackView: legacyRollbackView{State: "not_frozen", Items: []wechatapp.RollbackReceipt{}}}
	archive, err := s.legacyReceiptArchive(id)
	if err != nil {
		return view, err
	}
	w, err := s.worker(id, false)
	if err != nil {
		return view, err
	}
	if w == nil {
		return view, apphost.ErrNotFound
	}
	ref := s.connectionRef(id, archive.Plan.Account)
	view.worker, view.ref = w, ref
	w.mu.Lock()
	proof := w.policy.LegacyActivation
	view.Eligible = proof != nil && proof.Connection == ref && proof.ArchiveDigest == archive.Plan.Digest && w.policy.LegacyArchiveDigest == archive.Plan.Digest
	if !view.Eligible {
		w.mu.Unlock()
		if freeze {
			return view, apphost.ErrLifecycleCutover
		}
		return view, nil
	}
	if freeze && w.policy.RollbackFrozenAt.IsZero() {
		if ctx.Err() != nil || s.ctx.Err() != nil {
			w.mu.Unlock()
			return view, apphost.ErrLifecycleConflict
		}
		next := w.policy
		next.Paused, next.RollbackFrozenAt = true, time.Now().UTC()
		if err := w.changePolicy(next); err != nil {
			w.mu.Unlock()
			return view, err
		}
	}
	view.FrozenAt = w.policy.RollbackFrozenAt
	if view.FrozenAt.IsZero() {
		w.mu.Unlock()
		return view, nil
	}
	// The pause is durable even if the caller disconnects while the worker is
	// exiting. A subsequent read can observe the drain without changing state.
	run := w.run
	if freeze && run != nil {
		run.Cancel()
	}
	view.dataset = proof.TargetDataset
	if relocated := w.policy.RelocationActivation; relocated != nil {
		view.dataset = relocated.TargetDataset
	}
	w.mu.Unlock()
	workerStopped := run == nil
	if run != nil {
		select {
		case <-run.Done():
			workerStopped = true
		default:
		}
	}
	view.State = "draining"
	fence := s.platformFence(id)
	if !workerStopped || !fence.TryLock() {
		return view, nil
	}
	// The persisted policy denies new platform requests. Acquiring this lock
	// proves previously admitted requests have returned and closed their bodies.
	fence.Unlock()
	view.State = "frozen"
	return view, nil
}

func (target legacyRollbackTarget) checkpoint(after string, limit int) (wechatapp.RollbackCheckpoint, error) {
	w := target.worker
	w.deliveryMu.Lock()
	defer w.deliveryMu.Unlock()
	return wechatapp.ReadRollbackCheckpoint(w.deliveryJournal(), target.ref, target.FrozenAt, target.dataset, after, limit)
}

func (s *stationWechat) rollbackReceiptPage(ctx context.Context, target legacyRollbackTarget, revision, after string, limit int) (legacyRollbackView, error) {
	view := target.legacyRollbackView
	checkpoint, err := target.checkpoint(after, limit)
	if err != nil {
		return view, err
	}
	view.Snapshot = checkpoint.Snapshot()
	if revision != "" && revision != view.Snapshot {
		return view, errLegacyImportChanged
	}
	if checkpoint.Empty() {
		return view, nil
	}
	if checkpoint.Dataset() != target.dataset {
		view.Native = "dataset_mismatch"
		view.Items, err = checkpoint.Receipts(ctx, nil)
	} else {
		native, _, state := wechatapp.VerifyRollbackTransport(ctx, target.ref, target.dataset, s.WorkerSender)
		view.Native = state
		view.Items, err = checkpoint.Receipts(ctx, native)
	}
	if checkpoint.NextAfter() != "" {
		view.NextAfter = view.Snapshot + ":" + checkpoint.NextAfter()
	}
	return view, err
}
