package station

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	catalog "wechat-bot/plugins"
)

const portableRegistrationFile = "runtime/station-registration.json"

func portableManagedTool(id string) bool {
	switch id {
	case localAudioCppPackage, localRVCPackage, localYuEPackage, localTTSPackage, localComfyPackage, localOBSPackage:
		return true
	default:
		return false
	}
}

// An existing private registration (including a disabled one) takes
// precedence. Only a matching, built-in package can supply a portable default.
func portableRegistration(private, directory, id, editableRoot string, limit int64) (raw []byte, packageDir string, err error) {
	if _, err = os.Lstat(private); !errors.Is(err, os.ErrNotExist) {
		raw, err = localRVCReadRegular(private, limit)
		return raw, "", err
	}
	packageDir, err = portableToolDirectory(directory, id, editableRoot)
	if err != nil {
		return nil, "", err
	}
	raw, err = localRVCReadRegular(filepath.Join(packageDir, filepath.FromSlash(portableRegistrationFile)), limit)
	return raw, packageDir, err
}

func portableToolDirectory(directory, id, editableRoot string) (string, error) {
	packageDir, err := catalogPackageDir(directory, id)
	if err != nil || !filepath.IsAbs(packageDir) || !localRVCSafeDirectory(packageDir) ||
		(editableRoot != "" && localRVCPathsOverlap(packageDir, editableRoot)) {
		return "", os.ErrNotExist
	}
	contract, err := catalog.ContractFor(id)
	if err != nil {
		return "", err
	}
	manifest, err := localRVCReadRegular(filepath.Join(packageDir, "plugin.json"), 1<<20)
	digest := sha256.Sum256(manifest)
	if err != nil || hex.EncodeToString(digest[:]) != contract.ManifestSHA256 {
		return "", errors.New("便携工具包版本与工作站不匹配")
	}
	return packageDir, nil
}

func resolvePortablePaths(packageDir string, paths ...*string) error {
	for _, path := range paths {
		if !localComfySafeRelative(*path) {
			return errors.New("便携工具路径必须位于其工具包内")
		}
		absolute := filepath.Join(packageDir, filepath.FromSlash(*path))
		if !localRVCLinkFree(absolute) {
			return errors.New("便携工具依赖缺失或路径不安全")
		}
		*path = absolute
	}
	return nil
}

func portableDocumentRoot(directory, editableRoot, fallback string) string {
	dir, err := portableToolDirectory(directory, "document-workshop", editableRoot)
	if err != nil {
		return fallback
	}
	native := filepath.Join(dir, "runtime", "native")
	if !localRVCSafeRegular(filepath.Join(native, "runtime-manifest.json")) {
		return fallback
	}
	return native
}
