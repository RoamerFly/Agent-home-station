package station

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/fishidentity"
	"wechat-bot/internal/workspacefs"
)

const legacyRelocationTargetFile = "legacy-relocation-target.json"

type LegacyWechatRelocationRequest struct {
	DataDir, SourceDir, ConnectionID, AuditDigest string
	AcceptUnknown                                 bool
}

type LegacyWechatRelocationResult struct {
	State            string `json:"state"`
	ConnectionID     string `json:"connection"`
	RelocationTarget string `json:"relocation_target"`
	SourceFence      string `json:"source_fence_digest,omitempty"`
	TargetProfile    string `json:"target_profile_digest,omitempty"`
}

type legacyRelocationTarget struct {
	Version    int    `json:"version"`
	ID         string `json:"target"`
	Root       string `json:"root"`
	FishID     string `json:"fish_id"`
	OwnerID    string `json:"owner_id"`
	Connection string `json:"connection"`
	Archive    string `json:"archive_digest"`
	SourceStop string `json:"source_stop_digest"`
}

// CheckLegacyWechatRelocation prepares a target when SourceDir is omitted.
// With a source it verifies the handoff without enabling either connection.
func CheckLegacyWechatRelocation(ctx context.Context, req LegacyWechatRelocationRequest) (LegacyWechatRelocationResult, error) {
	var out LegacyWechatRelocationResult
	if runtime.GOOS != "windows" {
		return out, errors.New("微信搬迁核验仅支持 Windows 桌面")
	}
	dir, source := cutoverPath(req.DataDir), cutoverPath(req.SourceDir)
	if !sameCutoverPath(dir, dir) || !artifactID(req.ConnectionID, "wechat-") ||
		req.SourceDir == "" && (req.AuditDigest != "" || req.AcceptUnknown) ||
		req.SourceDir != "" && (!sameCutoverPath(source, source) || legacyImportOverlap(dir, source) || !legacyMemoryDigestValid(req.AuditDigest)) {
		return out, errors.New("目标目录、源站目录、微信连接或核对摘要无效")
	}
	root, err := workspacefs.OpenPath(dir)
	if err != nil {
		return out, errors.New("无法打开目标站数据目录")
	}
	defer root.Close()
	unlock, err := lockReleaseRoot(root, dir, stationNativeRunLockFile)
	if err != nil {
		return out, errors.New("目标站仍在使用，或停机锁不可用")
	}
	defer unlock()
	if _, err := root.Lstat(stationNativeReleaseFile); !errors.Is(err, os.ErrNotExist) {
		return out, errors.New("目标站带有封存记录，无法作为接收站")
	}
	workerRoot := filepath.Join("apps", wechatapp.ID, "workers")
	worker, err := workspacefs.OpenExistingDirectory(root, filepath.Join(workerRoot, req.ConnectionID))
	if err != nil {
		return out, errors.New("无法读取目标站的微信连接资料")
	}
	defer worker.Close()
	archive, archiveErr := readLegacyImportArchive(worker)
	policy, policyErr := readStationWechatPolicy(worker)
	proof := policy.LegacyActivation
	targetHost := stationWechat{root: filepath.Join(dir, workerRoot), dataDir: dir, installation: "wechat-station-native"}
	if archiveErr != nil || policyErr != nil || proof == nil || !policy.Paused || policy.RollbackFrozenAt.IsZero() ||
		wechatapp.ConnectionID(archive.Plan.Account) != req.ConnectionID || policy.LegacyArchiveDigest != archive.Plan.Digest ||
		!targetHost.activationBound(*proof, req.ConnectionID, archive.Plan, proof.TargetDataset) || legacyImportOverlap(dir, proof.OldRoot) {
		return out, errors.New("目标站缺少原连接一致的冻结记录和交接资料")
	}
	stopRaw, err := readLegacyImportFile(root, stationNativeStopFile, 16<<10)
	var stop stationNativeStop
	if err != nil || json.Unmarshal(stopRaw, &stop) != nil || stop.Version != 1 || stop.State != "stopped" ||
		!sameCutoverPath(stop.DataRoot, stop.DataRoot) || sameCutoverPath(stop.DataRoot, dir) ||
		stop.NativeServerID != proof.TargetDataset || !validRollbackEpoch(stop.Epoch) ||
		source != "" && !sameCutoverPath(stop.DataRoot, source) {
		return out, errors.New("目标站须保留源站正常停机记录，并在不同目录准备")
	}
	owner, err := fishidentity.New(dir).Read()
	if err != nil || !fishidentity.ValidFishID(owner.FishID) || owner.LocalOwner == nil || !fishidentity.ValidLocalOwner(*owner.LocalOwner) {
		return out, errors.New("目标站缺少可核对的鱼身份和本机主人")
	}
	want := legacyRelocationTarget{Version: 1, Root: dir, FishID: owner.FishID, OwnerID: owner.LocalOwner.ID,
		Connection: req.ConnectionID, Archive: archive.Plan.Digest, SourceStop: hexDigest(stopRaw)}
	raw, err := readLegacyImportFile(worker, legacyRelocationTargetFile, 4096)
	if errors.Is(err, os.ErrNotExist) && source == "" {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		var nonce [32]byte
		rand.Read(nonce[:])
		want.ID = hex.EncodeToString(nonce[:])
		stage := ".legacy-relocation-target-" + rand.Text() + ".tmp"
		defer worker.Remove(stage)
		if err := writeLegacyStagedFile(worker, stage, want); err != nil {
			return out, err
		}
		if err := worker.Rename(stage, legacyRelocationTargetFile); err != nil {
			return out, err
		}
	} else {
		var saved legacyRelocationTarget
		if err != nil || json.Unmarshal(raw, &saved) != nil || !legacyMemoryDigestValid(saved.ID) || !sameCutoverPath(saved.Root, dir) {
			return out, errors.New("目标站尚未准备，或搬迁标识已失效")
		}
		want.ID, want.Root = saved.ID, saved.Root
		if saved != want {
			return out, errors.New("目标站身份或接入资料已变化")
		}
	}
	out = LegacyWechatRelocationResult{State: "target_prepared", ConnectionID: req.ConnectionID, RelocationTarget: want.ID}
	if source == "" {
		return out, nil
	}
	state, targetProfile := targetHost.nativeImportReceipt(req.ConnectionID, archive.Plan.Account)
	if state != "import_receipt_matched" {
		return LegacyWechatRelocationResult{}, errors.New("目标站缺少与新目录匹配的原生档案导入回执")
	}
	verified, err := releaseLegacyWechat(ctx, LegacyWechatReleaseRequest{
		DataDir: source, ConnectionID: req.ConnectionID, AuditDigest: req.AuditDigest,
		AcceptUnknown: req.AcceptUnknown, RelocationTarget: want.ID,
	}, func(sourceWorker *os.Root, sourceStop string) error {
		if sourceStop != want.SourceStop {
			return errors.New("源站停机记录与目标副本不一致")
		}
		sourceOwner, err := fishidentity.New(source).Read()
		if err != nil || sourceOwner.FishID != want.FishID || sourceOwner.LocalOwner == nil || sourceOwner.LocalOwner.ID != want.OwnerID {
			return errors.New("源站和目标站的本机主人或鱼身份不一致")
		}
		for _, item := range []struct {
			name string
			max  int64
		}{{"worker.json", stationWechatPolicyLimit}, {legacyImportArchiveFile, legacyImportArchiveLimit}} {
			first, sourceErr := readLegacyImportFile(sourceWorker, item.name, item.max)
			second, targetErr := readLegacyImportFile(worker, item.name, item.max)
			if sourceErr != nil || targetErr != nil || !bytes.Equal(first, second) {
				return errors.New("目标站的冻结设置或归档与源站不一致")
			}
		}
		target := legacyRollbackTarget{
			legacyRollbackView: legacyRollbackView{State: "frozen", Eligible: true, FrozenAt: policy.RollbackFrozenAt},
			worker:             &stationWechatWorker{dir: filepath.Join(targetHost.root, req.ConnectionID)},
			ref:                proof.Connection, dataset: proof.TargetDataset,
		}
		auditRaw, err := readRollbackAudit(sourceWorker, req.AuditDigest)
		var audit rollbackAudit
		if err != nil || json.Unmarshal(auditRaw, &audit) != nil || !audit.matchesTarget(target) {
			return errors.New("源站投递核对报告与目标站不一致")
		}
		if current, err := targetHost.rollbackAuditCurrent(ctx, target, audit); err != nil || !current {
			return errors.New("目标站的投递或群操作记录与已封存报告不一致")
		}
		return nil
	})
	if err != nil {
		return LegacyWechatRelocationResult{}, err
	}
	out.State, out.SourceFence, out.TargetProfile = "source_verified", verified.ApplyDigest, hex.EncodeToString(targetProfile[:])
	return out, nil
}
