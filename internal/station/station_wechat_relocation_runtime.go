package station

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/workspacefs"
)

const (
	legacyRelocationRuntimeDir    = "relocated-runtime"
	legacyRelocationRuntimeMarker = "archive-binding.json"
)

// The marker is written only into an empty runtime. Retrying an interrupted
// apply can finish the marker, while a completed apply never resets work.
func prepareLegacyRelocationRuntime(worker *os.Root, digest string) error {
	runtime, err := workspacefs.EnsureDirectory(worker, legacyRelocationRuntimeDir)
	if err != nil {
		return err
	}
	defer runtime.Close()
	if saved, err := readLegacyRelocationRuntimeMarker(runtime); err == nil && saved == digest {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("新投递目录已有不同的归档绑定")
	}
	dir, err := runtime.Open(".")
	if err != nil {
		return err
	}
	names, err := dir.Readdirnames(1)
	dir.Close()
	if err != io.EOF || len(names) != 0 {
		return errors.New("未绑定的新投递目录须为空")
	}
	stage := ".legacy-relocation-runtime-" + rand.Text() + ".tmp"
	defer worker.Remove(stage)
	if err := writeLegacyStagedFile(worker, stage, digest); err != nil {
		return err
	}
	return worker.Rename(stage, filepath.Join(legacyRelocationRuntimeDir, legacyRelocationRuntimeMarker))
}

func readLegacyRelocationRuntimeMarker(runtime *os.Root) (string, error) {
	raw, err := readLegacyImportFile(runtime, legacyRelocationRuntimeMarker, 128)
	if err != nil {
		return "", err
	}
	var digest string
	if json.Unmarshal(raw, &digest) != nil || !legacyMemoryDigestValid(digest) {
		return "", apphost.ErrUnavailable
	}
	return digest, nil
}

func (s *stationWechat) relocationRuntime(worker *os.Root, id string) (string, error) {
	evidence, err := s.readRelocationEvidence(worker, id)
	if errors.Is(err, apphost.ErrNotFound) {
		// An orphaned runtime must never select the copied queue.
		if _, statErr := worker.Lstat(legacyRelocationRuntimeDir); errors.Is(statErr, os.ErrNotExist) {
			return "", nil
		}
	}
	if err != nil {
		return "", apphost.ErrUnavailable
	}
	if err := checkLegacyRelocationRuntime(worker, evidence.ApplyDigest); err != nil {
		return "", err
	}
	return filepath.Join(s.root, id, legacyRelocationRuntimeDir), nil
}

func checkLegacyRelocationRuntime(worker *os.Root, digest string) error {
	runtime, err := workspacefs.OpenExistingDirectory(worker, legacyRelocationRuntimeDir)
	if err != nil {
		return apphost.ErrUnavailable
	}
	defer runtime.Close()
	if saved, err := readLegacyRelocationRuntimeMarker(runtime); err != nil || saved != digest {
		return apphost.ErrUnavailable
	}
	return nil
}
