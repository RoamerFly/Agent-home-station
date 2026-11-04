package admin

import (
	"os"
	"path/filepath"
	"testing"

	"wechat-bot/internal/companion"
)

func startupAssetDocumentCount(t *testing.T, manager *Manager, asset mediaAsset) int {
	t.Helper()
	page, err := manager.memory.SearchGlobal(t.Context(), companion.GlobalQuery{
		Labels: map[string]string{asset.Scope: "合成群"}, Query: asset.ID, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	return page.Total
}

func reopenAssetStartupManager(t *testing.T, dir string, native *fakeNative) *Manager {
	t.Helper()
	manager, err := NewManager(dir, "", native)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

// A recovery may bring back memory.sqlite before its separate media catalogue.
// The latter is an availability fault, not evidence that every existing asset
// document was deleted. Conversely, a real [] remains the explicit deletion
// signal consumed by SyncDocuments.
func TestMemoryAssetIndexStartupRequiresExplicitValidManifest(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		manifest   []byte
		remove     bool
		wantDocs   int
		wantAbsent bool
	}{
		{name: "missing manifest preserves index", remove: true, wantDocs: 1, wantAbsent: true},
		{name: "null manifest preserves index", manifest: []byte("null"), wantDocs: 1},
		{name: "damaged manifest preserves index", manifest: []byte("{not-json"), wantDocs: 1},
		{name: "explicit empty manifest clears index", manifest: []byte("[]"), wantDocs: 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			dir := t.TempDir()
			native := &fakeNative{}
			manager := reopenAssetStartupManager(t, dir, native)
			asset := syntheticIndexAsset()
			if err := manager.saveAssets([]mediaAsset{asset}); err != nil {
				t.Fatal(err)
			}
			if got := startupAssetDocumentCount(t, manager, asset); got != 1 {
				t.Fatalf("initial asset document count: got %d, want 1", got)
			}
			if err := manager.Close(); err != nil {
				t.Fatal(err)
			}
			manifest := filepath.Join(dir, "media-assets.json")
			if scenario.remove {
				if err := os.Remove(manifest); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(manifest, scenario.manifest, 0600); err != nil {
				t.Fatal(err)
			}

			manager = reopenAssetStartupManager(t, dir, native)
			if got := startupAssetDocumentCount(t, manager, asset); got != scenario.wantDocs {
				t.Fatalf("startup document count: got %d, want %d", got, scenario.wantDocs)
			}
			if scenario.wantAbsent {
				if _, err := os.Stat(manifest); !os.IsNotExist(err) {
					t.Fatalf("startup recreated missing manifest: %v", err)
				}
			}
			if string(scenario.manifest) == "null" {
				items, err := manager.loadAssets()
				if err != nil || len(items) != 0 {
					t.Fatal("legacy empty catalogue is no longer readable", err)
				}
				if err = manager.saveAssets(items); err != nil {
					t.Fatal(err)
				}
				if raw, err := os.ReadFile(manifest); err != nil || string(raw) != "[]" {
					t.Fatal("explicitly saving an empty catalogue must persist []", err)
				}
			}
		})
	}
}
