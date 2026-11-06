package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

// legacyActivation is private host evidence, not a platform credential. The
// target dataset survives a restart. Transport ownership is checked anew.
type legacyActivation struct {
	ArchiveDigest      string                `json:"archive_digest"`
	ConfirmationDigest string                `json:"confirmation_digest"`
	Connection         apphost.ConnectionRef `json:"connection"`
	OldRoot            string                `json:"old_root"`
	StopDigest         string                `json:"stop_digest"`
	ProfileDigest      string                `json:"profile_digest"`
	TargetDataset      string                `json:"target_dataset"`
	ConfirmedAt        time.Time             `json:"confirmed_at"`
}

func (a legacyActivation) valid() bool {
	return legacyMemoryDigestValid(a.ArchiveDigest) && legacyMemoryDigestValid(a.ConfirmationDigest) &&
		legacyMemoryDigestValid(a.StopDigest) && legacyMemoryDigestValid(a.ProfileDigest) &&
		filepath.IsAbs(a.OldRoot) && filepath.Clean(a.OldRoot) == a.OldRoot &&
		identity.ValidPathID(a.Connection.AccountID) && identity.ValidPathID(a.TargetDataset) && !a.ConfirmedAt.IsZero()
}

type legacyActivationView struct {
	legacyCutover
	Status          string `json:"status"`
	Digest          string `json:"digest,omitempty"`
	Reason          string `json:"reason,omitempty"`
	ReplyCount      int    `json:"reply_count"`
	PendingReceipts int    `json:"pending_receipts"`
	ModelReady      bool   `json:"model_ready"`
}

type legacyActivationInput struct {
	Digest           string `json:"digest"`
	SameIdentity     bool   `json:"same_identity"`
	ReviewedReceipts bool   `json:"reviewed_receipts"`
}

type legacyActivationSource struct {
	*linuxnative.Sender
	check func() error
}

func (s legacyActivationSource) Events(ctx context.Context, after string) (linuxnative.EventPage, error) {
	if err := s.check(); err != nil {
		return linuxnative.EventPage{}, err
	}
	return s.Sender.Events(ctx, after)
}

func (s *stationWechat) legacyActivationRoutes(server *websession.Store, mux *http.ServeMux, path string) {
	handle := server.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var input legacyActivationInput
		if r.Method == http.MethodPost {
			if !websession.Decode(w, r, &input) {
				return
			}
			if !legacyMemoryDigestValid(input.Digest) || !input.SameIdentity {
				websession.Fail(w, http.StatusBadRequest, "请重新核对启用条件并确认实际微信身份")
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		view, err := s.legacyActivation(ctx, r.PathValue("connection"), input)
		switch {
		case err != nil:
			legacyImportFailure(w, err)
		case input.Digest != "" && view.Status != "confirmed":
			websession.Fail(w, http.StatusConflict, view.Reason)
		default:
			websession.Respond(w, http.StatusOK, view)
		}
	})
	mux.HandleFunc("GET "+path, handle)
	mux.HandleFunc("POST "+path, handle)
}

func (s *stationWechat) legacyActivation(ctx context.Context, id string, input legacyActivationInput) (legacyActivationView, error) {
	view := legacyActivationView{Status: "blocked", legacyCutover: legacyCutover{
		LegacyHost: "pending", NativeProfile: "pending", TargetLogin: "pending", TargetIdentity: "owner_check_pending",
	}}
	blocked := func(reason string) (legacyActivationView, error) {
		view.Reason = reason
		return view, nil
	}
	archive, err := s.legacyReceiptArchive(id)
	if err != nil {
		return view, err
	}
	for _, receipt := range archive.Receipts {
		if legacyReceiptPending(receipt) {
			view.PendingReceipts++
		}
	}
	native, err := s.WorkerSender(ctx, id)
	if err != nil || native.Account() != archive.Plan.Account {
		return blocked("当前微信原生连接暂不可核对")
	}
	w, err := s.worker(id, false)
	if err != nil {
		return view, err
	}
	if w == nil {
		return blocked("请先完成旧版接入并保存回复范围")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.policy.LegacyArchiveDigest != "" && w.policy.LegacyArchiveDigest != archive.Plan.Digest {
		return blocked("旧接入摘要与工作器记录不一致")
	}
	_, modelErr := s.modelPersona()
	view.ModelReady, view.ReplyCount = modelErr == nil, len(w.policy.Grants)
	daemon, err := native.DaemonStatus(ctx)
	if err != nil {
		return blocked("当前微信原生实例暂不可核对")
	}
	account := native.Account()
	status, _ := native.Status(ctx)
	view.TargetLogin = targetLoginState(status, account)
	view.NativeProfile, _ = s.nativeImportEvidence(id, account, status)
	if prior := w.policy.LegacyActivation; prior != nil && s.activationMatches(*prior, id, archive.Plan, daemon.ServerID) {
		view.LegacyHost, view.TargetIdentity = "stop_record_matched", "owner_confirmed"
		if input.Digest != "" && input.Digest != prior.ConfirmationDigest {
			return blocked("启用确认记录已变化，请重新核对")
		}
		if reason := activationMissing(view); reason != "" {
			return blocked(reason)
		}
		view.Status = "confirmed"
		return view, nil
	}
	if w.run != nil || !w.policy.Paused {
		return blocked("请先暂停此连接并等待工作器退出")
	}
	snapshot, source, err := s.openLegacyImport()
	if err != nil {
		return blocked("请保留原隔离快照及旧宿主目录，完成启用核对后再拆离快照")
	}
	defer snapshot.Close()
	cutover, evidence := s.checkLegacyCutover(ctx, id, native, snapshot, source, archive.LegacyServer)
	view.legacyCutover = *cutover
	if reason := activationMissing(view); reason != "" {
		return blocked(reason)
	}
	// The original archive stays authoritative. If the stopped source now
	// shows different delivery evidence or a different legacy policy, an
	// activation cannot silently abandon newly discovered records.
	latest, err := s.previewLegacyImport(ctx, id)
	if err != nil || !legacyMemoryCutoverReady(latest.Plan.Cutover) || !sameLegacyActivationSnapshot(archive, latest) {
		return blocked("旧投递快照或账号规则与已保存的接入摘要不同，请先核对差异")
	}
	proof := legacyActivation{
		ArchiveDigest: archive.Plan.Digest, Connection: s.connectionRef(id, account), OldRoot: cutoverPath(source.LegacyHostRoot),
		StopDigest: hex.EncodeToString(evidence[0][:]), ProfileDigest: hex.EncodeToString(evidence[1][:]), TargetDataset: daemon.ServerID,
		ConfirmedAt: time.Now().UTC(),
	}
	proof.ConfirmationDigest = localCheckpointKey("legacy-activation-v1", proof.ArchiveDigest, proof.Connection,
		proof.OldRoot, proof.StopDigest, proof.ProfileDigest, daemon, latest.Plan.Digest, w.policy.Grants, view.PendingReceipts)
	if input.Digest == "" {
		view.Status, view.Digest = "ready", proof.ConfirmationDigest
		return view, nil
	}
	if input.Digest != proof.ConfirmationDigest {
		return blocked("启用条件已变化，请重新核对")
	}
	if view.PendingReceipts != 0 && !input.ReviewedReceipts {
		return blocked("请确认未核对的旧回执只保留归档")
	}
	fresh, daemonErr := native.DaemonStatus(ctx)
	login, loginErr := native.Status(ctx)
	if daemonErr != nil || fresh != daemon || loginErr != nil || login.StorageMode != "managed" || targetLoginState(login, account) != "online" ||
		!s.activationMatches(proof, id, archive.Plan, fresh.ServerID) || ctx.Err() != nil || s.ctx.Err() != nil {
		return blocked("原生实例或切换证据已变化，请重新核对")
	}
	next := w.policy
	next.LegacyArchiveDigest, next.LegacyActivation = archive.Plan.Digest, &proof
	if err := w.save(next); err != nil {
		return view, err
	}
	view.Status, view.TargetIdentity = "confirmed", "owner_confirmed"
	return view, nil
}

func activationMissing(view legacyActivationView) string {
	switch {
	case view.LegacyHost != "stop_record_matched":
		return "旧宿主停机记录缺失或已变化，请核对旧桌面已正常停止"
	case view.NativeProfile != "import_receipt_matched":
		return "微信原生档案的导入凭证尚未匹配"
	case view.TargetLogin != "online":
		return "目标微信连接尚未在线"
	case !view.ModelReady:
		return "请先完成本机模型配置"
	case view.ReplyCount == 0:
		return "请先保存明确的回复范围"
	default:
		return ""
	}
}

func sameLegacyActivationSnapshot(saved, current legacyImportArchive) bool {
	return saved.LegacyServer == current.LegacyServer && saved.Plan.Account == current.Plan.Account &&
		(saved.SourceRevision == "" || saved.SourceRevision == current.SourceRevision) &&
		saved.Plan.LegacyEnabled == current.Plan.LegacyEnabled && saved.Plan.LegacyPaused == current.Plan.LegacyPaused &&
		slices.Equal(saved.Plan.SuggestedThreads, current.Plan.SuggestedThreads) && slices.Equal(saved.Plan.Omitted, current.Plan.Omitted) &&
		localCheckpointKey(saved.Receipts) == localCheckpointKey(current.Receipts) &&
		(len(saved.ReceiptSources) == 0 || localCheckpointKey(saved.ReceiptSources) == localCheckpointKey(current.ReceiptSources))
}

func (s *stationWechat) activationMatches(a legacyActivation, id string, plan legacyImportPlan, dataset string) bool {
	return a.valid() && a.ArchiveDigest == plan.Digest && a.Connection == s.connectionRef(id, plan.Account) && a.TargetDataset == dataset &&
		s.localActivationEvidence(id, a)
}

func (s *stationWechat) localActivationEvidence(id string, a legacyActivation) bool {
	if legacyImportOverlap(a.OldRoot, s.dataDir) || legacyImportOverlap(a.OldRoot, s.workspaceRoot) {
		return false
	}
	root, err := workspacefs.OpenPath(a.OldRoot)
	if err != nil {
		return false
	}
	defer root.Close()
	marker, err := readLegacyImportFile(root, legacyStopMarkerFile, 16<<10)
	if err != nil || hexDigest(marker) != a.StopDigest {
		return false
	}
	profile, digest := s.nativeImportReceipt(id, a.Connection.AccountID)
	return profile == "import_receipt_matched" && hex.EncodeToString(digest[:]) == a.ProfileDigest
}

func hexDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Called while the worker is locked, before any automatic processing begins.
func (s *stationWechat) legacyActivationGuard(ctx context.Context, id string, native *linuxnative.Sender, w *stationWechatWorker) (func() bool, error) {
	root, err := workspacefs.OpenPath(w.dir)
	if err != nil {
		return nil, apphost.ErrLifecycleCutover
	}
	defer root.Close()
	archive, err := readLegacyImportArchive(root)
	if errors.Is(err, os.ErrNotExist) && w.policy.LegacyActivation == nil && w.policy.LegacyArchiveDigest == "" {
		return nil, nil
	}
	if err != nil || w.policy.LegacyActivation == nil || archive.Plan.Account != native.Account() ||
		w.policy.LegacyArchiveDigest != archive.Plan.Digest {
		return nil, apphost.ErrLifecycleCutover
	}
	proof := *w.policy.LegacyActivation
	daemon, err := native.DaemonStatus(ctx)
	if err != nil || !s.activationMatches(proof, id, archive.Plan, daemon.ServerID) {
		return nil, apphost.ErrLifecycleCutover
	}
	status, err := native.Status(ctx)
	if err != nil || status.AccountID != native.Account() || status.StorageMode != "managed" {
		return nil, apphost.ErrLifecycleCutover
	}
	return func() bool { return s.localActivationEvidence(id, proof) }, nil
}
