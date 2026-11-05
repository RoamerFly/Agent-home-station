package devicespec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyImportPreservesRawHashTaskStateAndFiles(t *testing.T) {
	dir := t.TempDir()
	settings := Defaults()
	task := Task{ID: "device-one", RequestHash: "original-request", State: "running", Source: Source{Account: "synthetic-account", Sender: "original-owner"}}
	settingsRaw, _ := json.Marshal(settings)
	settingsRaw = append(settingsRaw, '\n')
	tasksRaw, _ := json.Marshal(map[string]Task{task.ID: task})
	for name, body := range map[string][]byte{"settings.json": settingsRaw, "tasks.json": tasksRaw} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	value, found, err := ReadLegacyImport(dir)
	if err != nil || !found || value.ManifestVersion != 1 || value.Tasks[task.ID].State != "running" || value.Tasks[task.ID].Source != task.Source {
		t.Fatal("read-only import altered task metadata", value, found, err)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("mobile-legacy-v1\x00"))
	for _, body := range [][]byte{settingsRaw, tasksRaw} {
		_, _ = hash.Write([]byte{1})
		_, _ = hash.Write(body)
		_, _ = hash.Write([]byte{0})
	}
	if value.SourceHash != hex.EncodeToString(hash.Sum(nil)) {
		t.Fatal("legacy raw-byte fingerprint changed")
	}
	for name, want := range map[string][]byte{"settings.json": settingsRaw, "tasks.json": tasksRaw} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("legacy source was rewritten", name, err)
		}
	}
	missing := filepath.Join(t.TempDir(), "absent")
	if _, found, err := ReadLegacyImport(missing); err != nil || found {
		t.Fatal("missing source was not a read-only absence", found, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("read-only import created legacy directories", err)
	}
}

func TestPinnedLegacySourceChangeIsRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"enabled":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := openLegacyRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, err := readOptionalLegacy(root, "settings.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"enabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = verifyOptionalLegacy(root, before); err == nil {
		t.Fatal("changed legacy source passed verification")
	}
}
