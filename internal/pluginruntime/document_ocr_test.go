package pluginruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifiedOCRRuntimeBindsBinaryAndLanguageModel(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{}
	for relative, body := range map[string]string{
		"tesseract/tesseract.exe":                "synthetic executable",
		"tesseract/tessdata/chi_sim.traineddata": "synthetic language model",
	} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		files[relative] = hex.EncodeToString(sum[:])
	}
	manifest, _ := json.Marshal(map[string]any{"components": map[string]any{"tesseract_chi_sim": true}, "files": files})
	path := filepath.Join(root, "runtime-manifest.json")
	if err := os.WriteFile(path, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if !verifiedOCRRuntime(root, path) {
		t.Fatal("verified runtime rejected")
	}
	if err := os.WriteFile(filepath.Join(root, "tesseract", "tesseract.exe"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if verifiedOCRRuntime(root, path) {
		t.Fatal("changed executable accepted")
	}
}

func TestDocumentOCRRejectsMalformedDigest(t *testing.T) {
	if isSHA256("not-a-hash") || isSHA256("ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789") {
		t.Fatal("malformed digest accepted")
	}
}
