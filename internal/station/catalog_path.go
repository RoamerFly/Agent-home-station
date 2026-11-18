package station

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"wechat-bot/internal/pluginhost"
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

// Discovery reads only the manifest. Installation snapshots and verifies the
// exact digest displayed to the administrator before accepting any code.
type discoveredToolPackage struct {
	pluginhost.Package
	directory string
	digest    string
}

var discoveredToolID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func discoverToolPackages(directory string) map[string]discoveredToolPackage {
	packages := map[string]discoveredToolPackage{}
	if directory == "" {
		return packages
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return packages
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return packages
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return packages
	}
	duplicates := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		file := filepath.Join(entry.Name(), "plugin.json")
		info, err := root.Lstat(file)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			continue
		}
		manifest, err := root.Open(file)
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(manifest, (1<<20)+1))
		manifest.Close()
		var p pluginhost.Package
		if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &p) != nil || !discoveredToolID.MatchString(p.ID) || strings.TrimSpace(p.Name) == "" {
			continue
		}
		if _, exists := packages[p.ID]; exists {
			duplicates[p.ID] = true
		}
		digest := sha256.Sum256(raw)
		packages[p.ID] = discoveredToolPackage{Package: p, directory: filepath.Join(directory, entry.Name()), digest: hex.EncodeToString(digest[:])}
	}
	for id := range duplicates {
		delete(packages, id)
	}
	for _, id := range []string{"mobile-control", "catalog", "registrations", "validation"} {
		delete(packages, id)
	}
	return packages
}

func (p discoveredToolPackage) compatible() bool {
	return p.Runtime.Transport == "mcp-stdio" && p.hostCompatible()
}

func (p discoveredToolPackage) hostCompatible() bool {
	return p.SchemaVersion == 1 && p.HostProtocol == pluginhost.Protocol && p.Status == "ready" &&
		p.Runtime.Entrypoint != "" && p.Files[p.Runtime.Entrypoint] != "" &&
		(slices.Contains(p.Platforms, runtime.GOOS) || slices.Contains(p.Platforms, runtime.GOOS+"-"+runtime.GOARCH))
}
