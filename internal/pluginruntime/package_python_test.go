package pluginruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"wechat-bot/internal/pluginhost"
)

func TestPackagePythonUsesMatchingToolID(t *testing.T) {
	root := t.TempDir()
	packageDir := filepath.Join(root, "audio-workshop-音频处理")
	runtimeDir := filepath.Join(packageDir, "runtime", "python")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	installed := pluginhost.Package{ID: "audio-workshop", Version: "0.1.2", Files: map[string]string{"runtime/main.py": "digest"}}
	descriptor, _ := json.Marshal(installed)
	if err := os.WriteFile(filepath.Join(packageDir, "plugin.json"), descriptor, 0600); err != nil {
		t.Fatal(err)
	}
	hashes := map[string]string{}
	for _, name := range []string{"python.exe", "pythonw.exe", "python313.zip"} {
		body := []byte("synthetic-" + name)
		if err := os.WriteFile(filepath.Join(runtimeDir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		hashes[name] = hex.EncodeToString(sum[:])
	}
	manifest, _ := json.Marshal(map[string]any{"files": hashes})
	if err := os.WriteFile(filepath.Join(runtimeDir, "runtime-manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	if got := packagePythonRoot(root, installed); got != runtimeDir {
		t.Fatalf("matching package Python = %q", got)
	}
	installed.Version = "0.1.1"
	if got := packagePythonRoot(root, installed); got != runtimeDir {
		t.Fatalf("package Python should remain available after catalog update: %q", got)
	}
	installed.ID = "different-tool"
	if got := packagePythonRoot(root, installed); got != "" {
		t.Fatalf("stale package Python selected: %q", got)
	}
}
