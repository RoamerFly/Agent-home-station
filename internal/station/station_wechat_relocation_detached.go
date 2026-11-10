package station

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
)

const (
	legacyDetachedFenceFile  = "legacy-relocation-detached.json"
	legacyDetachedFenceLimit = 8 << 10
)

func legacyDetachedFenceBinding(evidence legacyRelocationEvidence, proof legacyActivation) string {
	return localCheckpointKey("wechat-detached-fence-v1", evidence.ApplyDigest, proof)
}

func legacyDetachedFenceMatches(worker *os.Root, raw []byte, evidence legacyRelocationEvidence, proof legacyActivation) bool {
	var encrypted []byte
	if json.Unmarshal(raw, &encrypted) != nil || len(encrypted) == 0 {
		return false
	}
	digest, err := openLegacyDetachedFence(worker, encrypted)
	return err == nil && digest == legacyDetachedFenceBinding(evidence, proof)
}

// Call only after the locked source and receiver checks. This is a local
// attestation that the original source fence was observed on this receiver.
func saveLegacyDetachedFence(worker *os.Root, evidence legacyRelocationEvidence, proof legacyActivation) error {
	raw, err := readLegacyImportFile(worker, legacyDetachedFenceFile, legacyDetachedFenceLimit)
	if err == nil && legacyDetachedFenceMatches(worker, raw, evidence, proof) {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return errors.New("接收站已保存不同或无效的离线封存凭证")
	}
	encrypted, err := sealLegacyDetachedFence(worker, legacyDetachedFenceBinding(evidence, proof))
	if err != nil {
		return errors.New("无法将离线封存凭证绑定到本机和接收目录")
	}
	stage := ".legacy-detached-fence-" + rand.Text() + ".tmp"
	defer worker.Remove(stage)
	if err := writeLegacyStagedFile(worker, stage, encrypted); err != nil {
		return err
	}
	return worker.Rename(stage, legacyDetachedFenceFile)
}
