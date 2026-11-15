package station

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// catalogPackageDir resolves a package by the stable plugin ID in plugin.json.
// The on-disk folder may include a human-readable purpose suffix, for example
// "yue-作曲", while the package ID remains "yue" for protocol compatibility.
func catalogPackageDir(root, id string) (string, error) {
	if root == "" || id == "" {
		return "", errors.New("catalog_package_not_found")
	}
	direct := filepath.Join(root, id)
	if packageManifestMatches(filepath.Join(direct, "plugin.json"), id) {
		return direct, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(root, entry.Name())
		if packageManifestMatches(filepath.Join(candidate, "plugin.json"), id) {
			return candidate, nil
		}
	}
	return "", errors.New("catalog_package_not_found")
}

func packageManifestMatches(path, id string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var manifest struct {
		ID string `json:"id"`
	}
	return json.Unmarshal(raw, &manifest) == nil && manifest.ID == id
}
