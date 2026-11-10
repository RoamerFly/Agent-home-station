package station

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

type legacyRelocationReadiness struct {
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
	SourceFence string `json:"source_fence"`
	NativeLogin string `json:"native_login"`
	Frozen      bool   `json:"frozen"`
}

func (s *stationWechat) legacyRelocationReadinessRoute(auth *websession.Store, mux *http.ServeMux, path string) {
	mux.HandleFunc("GET "+path, auth.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		view, err := s.relocationReadiness(ctx, r.PathValue("connection"))
		if err != nil {
			legacyImportFailure(w, err)
			return
		}
		websession.Respond(w, http.StatusOK, view)
	}))
}

func (s *stationWechat) relocationReadiness(ctx context.Context, id string) (legacyRelocationReadiness, error) {
	view := legacyRelocationReadiness{State: "blocked", SourceFence: "unverified", NativeLogin: "pending"}
	blocked := func(reason string) (legacyRelocationReadiness, error) {
		view.Reason = reason
		return view, nil
	}
	if !artifactID(id, "wechat-") {
		return view, apphost.ErrNotFound
	}
	root, err := workspacefs.OpenPath(filepath.Join(s.root, id))
	if errors.Is(err, os.ErrNotExist) {
		return view, apphost.ErrNotFound
	}
	if err != nil {
		return view, apphost.ErrUnavailable
	}
	defer root.Close()
	evidence, err := s.readRelocationEvidence(root, id)
	if err != nil {
		return view, err
	}
	policy, err := readStationWechatPolicy(root)
	proof := policy.LegacyActivation
	view.Frozen = err == nil && policy.Paused && !policy.RollbackFrozenAt.IsZero()
	archive, archiveErr := readLegacyImportArchive(root)
	if !view.Frozen || proof == nil || !policy.RollbackFrozenAt.Equal(evidence.FrozenAt) || archiveErr != nil ||
		archive.Plan.Digest != evidence.Target.Archive || policy.LegacyArchiveDigest != evidence.Target.Archive ||
		!s.activationBound(*proof, id, archive.Plan, evidence.SourceDataset) ||
		wechatapp.ConnectionID(archive.Plan.Account) != id {
		return blocked("冻结状态或原微信连接记录已变化")
	}
	if err := checkLegacyRelocationRuntime(root, evidence.ApplyDigest); err != nil {
		return blocked("新投递目录未与原归档绑定")
	}
	if _, err := readRollbackAudit(root, evidence.AuditDigest); err != nil {
		return blocked("原投递核对报告缺失或已变化")
	}
	if !s.relocationSourceStillFenced(evidence, *proof) {
		return blocked("源站封存或旧微信桌面交接记录无法核对")
	}
	view.SourceFence = "verified"

	profile, profileDigest := s.nativeImportReceipt(id, archive.Plan.Account)
	if profile != "import_receipt_matched" || hex.EncodeToString(profileDigest[:]) != evidence.ProfileDigest {
		return blocked("接收站的原生档案导入回执已变化")
	}
	epoch := s.runningDesktopEpoch()
	if epoch == "" {
		return blocked("未取得接收站本次桌面运行记录")
	}
	native, err := s.stationWechatNative.WorkerSender(ctx, id)
	if err != nil {
		return blocked("接收站原生服务或账号暂不可核对")
	}
	if native.Account() != archive.Plan.Account {
		return blocked("接收站原生连接与原账号不一致")
	}
	daemon, err := native.DaemonStatus(ctx)
	if err != nil || !sameStationNativeRoot(s.dataDir, daemon) {
		return blocked("微信原生实例不属于接收站本次运行")
	}
	login, err := native.Status(ctx)
	if err != nil || login.AccountID != archive.Plan.Account || login.StorageMode != "managed" || !login.LoginObserved {
		return blocked("未取得接收站匹配账号的托管登录状态")
	}
	view.NativeLogin = targetLoginState(login, archive.Plan.Account)
	if view.NativeLogin != "online" {
		return blocked("接收站微信账号尚未在线")
	}
	latest, err := native.DaemonStatus(ctx)
	profile, profileDigest = s.nativeImportReceipt(id, archive.Plan.Account)
	if err != nil || latest != daemon || s.runningDesktopEpoch() != epoch ||
		profile != "import_receipt_matched" || hex.EncodeToString(profileDigest[:]) != evidence.ProfileDigest ||
		ctx.Err() != nil || s.ctx.Err() != nil {
		view.NativeLogin = "pending"
		return blocked("接收站实例或档案在核对期间已变化")
	}
	view.State = "owner_identity_check"
	return view, nil
}

func (s *stationWechat) relocationSourceStillFenced(evidence legacyRelocationEvidence, proof legacyActivation) bool {
	if legacyImportOverlap(s.dataDir, evidence.SourceDir) {
		return false
	}
	source, err := workspacefs.OpenPath(evidence.SourceDir)
	if err != nil {
		return false
	}
	defer source.Close()
	raw, err := readLegacyImportFile(source, stationNativeReleaseFile, 4096)
	var intent legacyReleaseIntent
	if err != nil || json.Unmarshal(raw, &intent) != nil || intent.Version != 1 ||
		intent.ConnectionID != evidence.Target.Connection || intent.AuditDigest != evidence.AuditDigest ||
		intent.ApplyDigest != evidence.SourceFence || intent.RelocationTarget != evidence.Target.ID {
		return false
	}
	stop, err := readLegacyImportFile(source, stationNativeStopFile, 16<<10)
	claim, _ := json.Marshal(legacyHandoverKey(proof))
	return err == nil && hexDigest(stop) == evidence.Target.SourceStop && intent.ClaimDigest == hexDigest(claim) &&
		intent.ApplyDigest == localCheckpointKey("wechat-relocation-fence-v1", proof, intent.AuditDigest,
			evidence.Target.SourceStop, intent.ClaimDigest, intent.AcceptUnknown, intent.RelocationTarget) &&
		s.legacyHandover(proof, handoverActive) == ""
}
