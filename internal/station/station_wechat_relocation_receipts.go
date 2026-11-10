package station

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/fishidentity"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const legacyRelocationEvidenceFile = "legacy-relocation-evidence.json"

type legacyRelocationEvidence struct {
	Version       int                    `json:"version"`
	Target        legacyRelocationTarget `json:"target"`
	SourceDir     string                 `json:"source_dir"`
	SourceFence   string                 `json:"source_fence_digest"`
	ProfileDigest string                 `json:"profile_digest"`
	AuditDigest   string                 `json:"audit_digest"`
	FrozenAt      time.Time              `json:"frozen_at"`
	SourceDataset string                 `json:"source_dataset"`
	ApplyDigest   string                 `json:"apply_digest"`
}

func (e legacyRelocationEvidence) digest() string {
	e.ApplyDigest = ""
	return localCheckpointKey("wechat-relocation-archive-v1", e)
}

func saveLegacyRelocationEvidence(root *os.Root, evidence legacyRelocationEvidence) error {
	prior, err := readLegacyImportFile(root, legacyRelocationEvidenceFile, 16<<10)
	if err == nil {
		var saved legacyRelocationEvidence
		if json.Unmarshal(prior, &saved) == nil && saved.ApplyDigest == evidence.ApplyDigest && saved.digest() == evidence.ApplyDigest {
			return nil
		}
		return errors.New("目标站已有不同的旧投递归档确认")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stage := ".legacy-relocation-evidence-" + rand.Text() + ".tmp"
	defer root.Remove(stage)
	if err := writeLegacyStagedFile(root, stage, evidence); err != nil {
		return err
	}
	return root.Rename(stage, legacyRelocationEvidenceFile)
}

func (s *stationWechat) readLegacyRelocationReceiptHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	result, err := s.legacyRelocationReceipt(r.PathValue("connection"), r.PathValue("key"))
	if err != nil {
		legacyImportFailure(w, err)
		return
	}
	websession.Respond(w, http.StatusOK, result)
}

func (s *stationWechat) legacyRelocationReceipt(id, key string) (wechatapp.RollbackReceipt, error) {
	var out wechatapp.RollbackReceipt
	if !artifactID(id, "wechat-") || !identity.ValidPathID(key) {
		return out, apphost.ErrNotFound
	}
	root, err := workspacefs.OpenPath(filepath.Join(s.root, id))
	if errors.Is(err, os.ErrNotExist) {
		return out, apphost.ErrNotFound
	}
	if err != nil {
		return out, apphost.ErrUnavailable
	}
	defer root.Close()
	evidence, err := s.readRelocationEvidence(root, id)
	if err != nil {
		return out, err
	}
	archive, err := readLegacyImportArchive(root)
	if err != nil || archive.Plan.Digest != evidence.Target.Archive || wechatapp.ConnectionID(archive.Plan.Account) != id {
		return out, apphost.ErrUnavailable
	}
	auditRaw, err := readRollbackAudit(root, evidence.AuditDigest)
	var audit rollbackAudit
	target := legacyRollbackTarget{
		legacyRollbackView: legacyRollbackView{FrozenAt: evidence.FrozenAt},
		ref:                s.connectionRef(id, archive.Plan.Account), dataset: evidence.SourceDataset,
	}
	if err != nil || json.Unmarshal(auditRaw, &audit) != nil || !audit.matchesTarget(target) {
		return out, apphost.ErrUnavailable
	}
	found := false
	for _, entry := range audit.Deliveries {
		if entry.IdempotencyKey != key {
			continue
		}
		if found {
			return wechatapp.RollbackReceipt{}, apphost.ErrUnavailable
		}
		out, found = entry, true
	}
	if !found {
		return out, apphost.ErrNotFound
	}
	out.Lookup = "relocation_snapshot"
	return out, nil
}

// readRelocationEvidence validates the private record without contacting either
// native service. Archived receipts stay available independently of the queue.
func (s *stationWechat) readRelocationEvidence(root *os.Root, id string) (legacyRelocationEvidence, error) {
	var evidence legacyRelocationEvidence
	raw, err := readLegacyImportFile(root, legacyRelocationEvidenceFile, 16<<10)
	if errors.Is(err, os.ErrNotExist) {
		return evidence, apphost.ErrNotFound
	}
	if err != nil || json.Unmarshal(raw, &evidence) != nil || evidence.Version != 1 || evidence.ApplyDigest != evidence.digest() ||
		evidence.Target.Connection != id || !sameCutoverPath(evidence.Target.Root, s.dataDir) || !legacyMemoryDigestValid(evidence.AuditDigest) {
		return evidence, apphost.ErrUnavailable
	}
	owner, err := fishidentity.New(s.dataDir).Read()
	if err != nil || owner.LocalOwner == nil || owner.FishID != evidence.Target.FishID || owner.LocalOwner.ID != evidence.Target.OwnerID {
		return evidence, apphost.ErrUnavailable
	}
	return evidence, nil
}
