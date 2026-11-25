package pluginruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"wechat-bot/internal/pluginhost"
)

// packagePythonRoot selects the interpreter provisioned beside an installed
// tool's catalog package. Older installations continue using the host runtime.
func packagePythonRoot(catalog string, installed pluginhost.Package) string {
	if catalog == "" {
		return ""
	}
	entries, err := os.ReadDir(catalog)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		packageDir := filepath.Join(catalog, entry.Name())
		data, err := os.ReadFile(filepath.Join(packageDir, "plugin.json"))
		if err != nil || len(data) > 1<<20 {
			continue
		}
		var descriptor struct { ID string `json:"id"` }
		if json.Unmarshal(data, &descriptor) != nil || descriptor.ID != installed.ID {
			continue
		}
		root := filepath.Join(packageDir, "runtime", "python")
		var manifest struct {
			Files map[string]string `json:"files"`
		}
		data, err = os.ReadFile(filepath.Join(root, "runtime-manifest.json"))
		if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &manifest) != nil {
			continue
		}
		verified := true
		for _, name := range []string{"python.exe", "pythonw.exe", "python313.zip"} {
			path := filepath.Join(root, name)
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				verified = false
				break
			}
			content, err := os.ReadFile(path)
			if err != nil {
				verified = false
				break
			}
			sum := sha256.Sum256(content)
			if hex.EncodeToString(sum[:]) != manifest.Files[name] {
				verified = false
				break
			}
		}
		if verified {
			return root
		}
	}
	return ""
}

func packageDocumentRuntimeReady(root string) bool {
	for _, name := range []string{"runtime-manifest.json", "python-packages", "fonts", "pdf-raster", "tesseract"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if name == "runtime-manifest.json" && !info.Mode().IsRegular() {
			return false
		}
		if name != "runtime-manifest.json" && !info.IsDir() {
			return false
		}
	}
	return true
}
